//! One running ShadowQUIC client behind a loopback SOCKS5 listener.
//!
//! The Xray core owns the routing, DNS and the tunnel; this crate only carries the
//! connections the core hands to a ShadowQUIC server. The core starts a client with a
//! JSON description, learns the loopback port and speaks SOCKS5 to it.

mod ffi;
mod log;

use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;
use shadowquic::Manager;
use shadowquic::config::{AuthUser, CongestionControl, ShadowQuicClientCfg, SocksServerCfg};
use shadowquic::dns::ResolverManager;
use shadowquic::shadowquic::outbound::ShadowQuicClient;
use shadowquic::socks::inbound::SocksServer;
use tokio::runtime::{Builder, Runtime};
use tokio::sync::oneshot;
use tokio::task::JoinHandle;

pub use ffi::{elarni_shadowquic_set_log, elarni_shadowquic_start, elarni_shadowquic_stop};
pub use log::set_log_sink;

/// How long `stop` waits for the client to close its connection and its tasks.
const STOP_TIMEOUT: Duration = Duration::from_secs(2);
/// QUIC needs 1200 bytes; a smaller value makes the transport refuse the configuration.
const MIN_MTU: u16 = 1200;

/// What the core sends. Unknown keys are refused so that a typo cannot silently
/// fall back to a default.
#[derive(Deserialize, Debug)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ClientConfig {
    /// Server as `ip:port`. The core resolves names itself, with its own resolver.
    pub server: String,
    pub server_name: String,
    pub username: String,
    pub password: String,
    #[serde(default)]
    pub alpn: Option<Vec<String>>,
    #[serde(default)]
    pub congestion_control: Option<String>,
    #[serde(default)]
    pub zero_rtt: Option<bool>,
    #[serde(default)]
    pub udp_over_stream: Option<bool>,
    #[serde(default)]
    pub initial_mtu: Option<u16>,
    #[serde(default)]
    pub min_mtu: Option<u16>,
    /// Linux and Android: routing mark of the socket to the server.
    #[serde(default)]
    pub fw_mark: Option<u32>,
    /// Windows: index of the interface the socket to the server leaves through.
    #[serde(default)]
    pub bind_interface_index: Option<u32>,
    /// Login of the loopback listener; other local programs must not be able to use it.
    pub socks_username: String,
    pub socks_password: String,
}

/// A running client. Dropping it stops the client.
pub struct Client {
    port: u16,
    stop: Option<oneshot::Sender<()>>,
    task: Option<JoinHandle<()>>,
    runtime: Option<Runtime>,
}

impl Client {
    /// Port of the SOCKS5 listener on 127.0.0.1.
    pub fn port(&self) -> u16 {
        self.port
    }

    /// False once the client has ended by itself.
    pub fn is_running(&self) -> bool {
        self.task.as_ref().is_some_and(|task| !task.is_finished())
    }

    pub fn stop(mut self) {
        self.shut_down();
    }

    fn shut_down(&mut self) {
        let Some(runtime) = self.runtime.take() else {
            return;
        };
        drop(self.stop.take());
        if let Some(task) = self.task.take() {
            let _ = runtime.block_on(async { tokio::time::timeout(STOP_TIMEOUT, task).await });
        }
        runtime.shutdown_timeout(STOP_TIMEOUT);
    }
}

impl Drop for Client {
    fn drop(&mut self) {
        self.shut_down();
    }
}

/// Starts a client from the JSON description. Must not be called from a tokio runtime.
pub fn start(config: &[u8]) -> Result<Client, String> {
    let config: ClientConfig = serde_json::from_slice(config)
        .map_err(|error| format!("invalid configuration: {error}"))?;
    let (client, socks) = build(config)?;
    let runtime = Builder::new_multi_thread()
        .worker_threads(worker_threads())
        .thread_name("elarni-shadowquic")
        .enable_all()
        .build()
        .map_err(|error| format!("cannot start the runtime: {error}"))?;
    let (stop, stopped) = oneshot::channel::<()>();
    let started = runtime.block_on(async move {
        let socks = SocksServer::new(socks)
            .await
            .map_err(|error| format!("cannot create the listener: {error}"))?;
        let mut bound = socks.bound_addr.subscribe();
        let client = ShadowQuicClient::new(client, Arc::new(ResolverManager::new()));
        let manager = Manager::single(Box::new(socks), Arc::new(client));
        let mut task = tokio::spawn(async move {
            let result = manager
                .run_until(async {
                    let _ = stopped.await;
                })
                .await;
            if let Err(error) = result {
                tracing::error!("the ShadowQUIC client ended: {error}");
            }
        });
        tokio::select! {
            biased;
            address = bound.wait_for(Option::is_some) => match address.map(|address| *address) {
                Ok(Some(address)) => Ok((address.port(), task)),
                _ => Err("the listener did not start".to_owned()),
            },
            _ = &mut task => Err("the listener did not start".to_owned()),
        }
    });
    match started {
        Ok((port, task)) => Ok(Client {
            port,
            stop: Some(stop),
            task: Some(task),
            runtime: Some(runtime),
        }),
        Err(error) => {
            runtime.shutdown_timeout(STOP_TIMEOUT);
            Err(error)
        }
    }
}

fn build(config: ClientConfig) -> Result<(ShadowQuicClientCfg, SocksServerCfg), String> {
    let server: SocketAddr = config
        .server
        .parse()
        .map_err(|_| "server must be an IP address with a port".to_owned())?;
    if server.port() == 0 {
        return Err("server port must not be 0".to_owned());
    }
    for (name, value) in [
        ("serverName", &config.server_name),
        ("username", &config.username),
        ("password", &config.password),
        ("socksUsername", &config.socks_username),
        ("socksPassword", &config.socks_password),
    ] {
        if value.is_empty() {
            return Err(format!("{name} must not be empty"));
        }
    }
    // SOCKS5 carries the login in one-byte length fields (RFC 1929).
    if config.socks_username.len() > 255 || config.socks_password.len() > 255 {
        return Err("the listener login is too long".to_owned());
    }
    // Read like the client's own configuration file, so that an option left out here
    // means what it means there (`Default` of the type differs: no ALPN, no 0-RTT).
    let mut client: ShadowQuicClientCfg = serde_json::from_value(serde_json::json!({
        "tag": "outbound",
        "username": config.username,
        "password": config.password,
        "addr": server.to_string(),
        "server-name": config.server_name,
    }))
    .map_err(|error| format!("invalid configuration: {error}"))?;
    if let Some(alpn) = config.alpn {
        if alpn
            .iter()
            .any(|protocol| protocol.is_empty() || protocol.len() > 255)
        {
            return Err("alpn holds an invalid protocol name".to_owned());
        }
        client.alpn = alpn;
    }
    if let Some(name) = config.congestion_control {
        client.congestion_control = match name.as_str() {
            "bbr" => CongestionControl::Bbr,
            "cubic" => CongestionControl::Cubic,
            "new-reno" => CongestionControl::NewReno,
            _ => return Err(format!("unsupported congestion control: {name}")),
        };
    }
    if let Some(zero_rtt) = config.zero_rtt {
        client.zero_rtt = zero_rtt;
    }
    if let Some(over_stream) = config.udp_over_stream {
        client.over_stream = over_stream;
    }
    if let Some(mtu) = config.initial_mtu {
        client.initial_mtu = mtu;
    }
    if let Some(mtu) = config.min_mtu {
        client.min_mtu = mtu;
    }
    if client.min_mtu < MIN_MTU || client.initial_mtu < client.min_mtu {
        return Err(format!(
            "the MTU must be at least {MIN_MTU} and the initial MTU not below the minimum"
        ));
    }
    apply_socket_binding(&mut client, config.fw_mark, config.bind_interface_index)?;
    limit_for_platform(&mut client);
    let socks = SocksServerCfg {
        tag: "inbound".to_owned(),
        default_outbound: None,
        bind_addr: SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0),
        users: vec![AuthUser {
            username: config.socks_username,
            password: config.socks_password,
        }],
    };
    Ok((client, socks))
}

fn apply_socket_binding(
    client: &mut ShadowQuicClientCfg,
    fw_mark: Option<u32>,
    interface_index: Option<u32>,
) -> Result<(), String> {
    if let Some(mark) = fw_mark {
        if !cfg!(any(target_os = "linux", target_os = "android")) {
            return Err("fwMark is not supported on this system".to_owned());
        }
        client.socket_opt.fw_mark = Some(mark);
    }
    if let Some(index) = interface_index {
        client.socket_opt.bind_interface = Some(shadowquic::config::Interface::Device(
            interface_name(index)?,
        ));
    }
    Ok(())
}

/// The client binds by interface name; Windows gives every interface a name derived
/// from its index-independent identifier, which `if_indextoname` returns.
#[cfg(windows)]
fn interface_name(index: u32) -> Result<String, String> {
    #[link(name = "iphlpapi")]
    unsafe extern "system" {
        fn if_indextoname(index: u32, name: *mut u8) -> *mut u8;
    }
    // NDIS_IF_MAX_STRING_SIZE is 256 characters plus the terminator.
    let mut name = [0u8; 512];
    // SAFETY: the buffer is larger than the longest name the function writes.
    let written = unsafe { if_indextoname(index, name.as_mut_ptr()) };
    if written.is_null() {
        return Err(format!("no interface has index {index}"));
    }
    let length = name.iter().position(|byte| *byte == 0).unwrap_or(0);
    let name = std::str::from_utf8(&name[..length]).map_err(|_| "invalid interface name")?;
    if name.is_empty() {
        return Err(format!("no interface has index {index}"));
    }
    Ok(name.to_owned())
}

#[cfg(not(windows))]
fn interface_name(_index: u32) -> Result<String, String> {
    Err("bindInterfaceIndex is not supported on this system".to_owned())
}

/// An iOS network extension is ended by the system above about 50 MB, and the Go core
/// in the same process already plans for 30 MB. Unread data of slow readers stays in the
/// receive windows, so they are bounded there; every other system keeps the client's own
/// values.
#[cfg(target_os = "ios")]
fn limit_for_platform(client: &mut ShadowQuicClientCfg) {
    client.stream_receive_window = IOS_STREAM_RECEIVE_WINDOW;
    client.receive_window = Some(IOS_RECEIVE_WINDOW);
}

#[cfg(not(target_os = "ios"))]
fn limit_for_platform(_client: &mut ShadowQuicClientCfg) {}

#[cfg(target_os = "ios")]
const IOS_STREAM_RECEIVE_WINDOW: u64 = 1024 * 1024;
#[cfg(target_os = "ios")]
const IOS_RECEIVE_WINDOW: u64 = 4 * 1024 * 1024;

fn worker_threads() -> usize {
    let cores = std::thread::available_parallelism().map_or(2, usize::from);
    if cfg!(target_os = "ios") {
        2
    } else {
        cores.clamp(2, 8)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(change: impl FnOnce(&mut serde_json::Value)) -> Vec<u8> {
        let mut value = serde_json::json!({
            "server": "127.0.0.1:443",
            "serverName": "example.com",
            "username": "user",
            "password": "pass",
            "socksUsername": "local",
            "socksPassword": "secret",
        });
        change(&mut value);
        serde_json::to_vec(&value).unwrap()
    }

    fn built(change: impl FnOnce(&mut serde_json::Value)) -> Result<ShadowQuicClientCfg, String> {
        let config: ClientConfig =
            serde_json::from_slice(&config(change)).map_err(|error| error.to_string())?;
        build(config).map(|(client, _)| client)
    }

    #[test]
    fn absent_options_keep_the_values_of_the_client_configuration_file() {
        let client = built(|_| {}).unwrap();
        assert_eq!(client.alpn, ["h3"]);
        assert_eq!((client.initial_mtu, client.min_mtu), (1300, 1290));
        assert!(client.zero_rtt);
        assert!(!client.over_stream);
        assert!(client.congestion_control == CongestionControl::Bbr);
        assert_eq!(client.keep_alive_interval, 0);
        assert!(client.gso && client.mtu_discovery);
        assert_eq!(client.socket_opt.fw_mark, None);
        assert!(client.socket_opt.bind_interface.is_none());
        #[cfg(not(target_os = "ios"))]
        {
            let stock = ShadowQuicClientCfg::default();
            assert_eq!(client.stream_receive_window, stock.stream_receive_window);
            assert_eq!(client.receive_window, None);
        }
    }

    #[test]
    fn options_reach_the_client() {
        let client = built(|value| {
            value["alpn"] = serde_json::json!(["h3", "h2"]);
            value["congestionControl"] = "cubic".into();
            value["zeroRtt"] = false.into();
            value["udpOverStream"] = true.into();
            value["initialMtu"] = 1400.into();
            value["minMtu"] = 1280.into();
        })
        .unwrap();
        assert_eq!(client.alpn, ["h3", "h2"]);
        assert!(client.congestion_control == CongestionControl::Cubic);
        assert!(!client.zero_rtt);
        assert!(client.over_stream);
        assert_eq!((client.initial_mtu, client.min_mtu), (1400, 1280));
        assert_eq!(client.addr, "127.0.0.1:443");
        assert_eq!(client.server_name, "example.com");
    }

    #[test]
    fn invalid_configurations_are_refused() {
        type Change = Box<dyn FnOnce(&mut serde_json::Value)>;
        let cases: [(&str, Change); 11] = [
            (
                "a name as server",
                Box::new(|v| v["server"] = "example.com:443".into()),
            ),
            ("port 0", Box::new(|v| v["server"] = "127.0.0.1:0".into())),
            ("no port", Box::new(|v| v["server"] = "127.0.0.1".into())),
            (
                "empty server name",
                Box::new(|v| v["serverName"] = "".into()),
            ),
            ("empty password", Box::new(|v| v["password"] = "".into())),
            (
                "empty listener login",
                Box::new(|v| v["socksUsername"] = "".into()),
            ),
            ("unknown key", Box::new(|v| v["unknown"] = 1.into())),
            (
                "unknown congestion control",
                Box::new(|v| v["congestionControl"] = "copa".into()),
            ),
            ("small MTU", Box::new(|v| v["minMtu"] = 1100.into())),
            (
                "initial MTU below the minimum",
                Box::new(|v| v["initialMtu"] = 1250.into()),
            ),
            (
                "empty ALPN name",
                Box::new(|v| v["alpn"] = serde_json::json!([""])),
            ),
        ];
        for (name, change) in cases {
            assert!(built(change).is_err(), "{name} was accepted");
        }
        assert!(start(b"not json").is_err());
    }

    #[test]
    fn socket_binding_is_refused_where_it_cannot_be_applied() {
        let mark = built(|value| value["fwMark"] = 255.into());
        if cfg!(any(target_os = "linux", target_os = "android")) {
            assert_eq!(mark.unwrap().socket_opt.fw_mark, Some(255));
        } else {
            assert!(mark.is_err());
        }
        #[cfg(not(windows))]
        assert!(built(|value| value["bindInterfaceIndex"] = 1.into()).is_err());
    }

    /// Index 1 is the loopback interface on every Windows version.
    #[cfg(windows)]
    #[test]
    fn an_interface_index_becomes_the_name_the_client_binds_by() {
        let client = built(|value| value["bindInterfaceIndex"] = 1.into()).unwrap();
        let Some(shadowquic::config::Interface::Device(name)) = client.socket_opt.bind_interface
        else {
            panic!("no interface name");
        };
        assert!(!name.is_empty());
        assert!(built(|value| value["bindInterfaceIndex"] = 0x7fff_fff0u32.into()).is_err());
    }
}
