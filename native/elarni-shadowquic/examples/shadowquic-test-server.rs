//! A ShadowQUIC server for tests of the client and of the core's outbound.
//!
//! `shadowquic-test-server [LISTEN [SOCKS]]`: listens on LISTEN (`127.0.0.1:0` when left
//! out), prints `{"port":N}` and serves the user `user` / `pass` under the server name
//! `localhost` until standard input ends. Requests leave directly, so a test reaches its
//! own loopback targets through it; with SOCKS they leave through that SOCKS5 server, where
//! a tunnel check decides where its made-up destinations lead.

use std::io::Read;
use std::net::SocketAddr;
use std::sync::Arc;

use shadowquic::config::{
    AuthUser, CongestionControl, JlsUpstream, ShadowQuicServerCfg, SocksClientCfg,
};
use shadowquic::direct::outbound::DirectOut;
use shadowquic::dns::ResolverManager;
use shadowquic::shadowquic::inbound::ShadowQuicServer;
use shadowquic::socks::outbound::SocksClient;
use shadowquic::{Manager, Outbound};

#[tokio::main(flavor = "multi_thread", worker_threads = 2)]
async fn main() {
    let mut arguments = std::env::args().skip(1);
    let listen = arguments.next().unwrap_or_else(|| "127.0.0.1:0".into());
    let outbound: Arc<dyn Outbound> = match arguments.next() {
        Some(socks) => {
            let config: SocksClientCfg =
                serde_json::from_value(serde_json::json!({"tag": "outbound", "addr": socks}))
                    .expect("invalid SOCKS address");
            Arc::new(SocksClient::new(config, Arc::new(ResolverManager::new())))
        }
        None => Arc::new(DirectOut::default()),
    };
    let address: SocketAddr = std::net::UdpSocket::bind(listen.as_str())
        .and_then(|socket| socket.local_addr())
        .expect("no free port");
    let server = ShadowQuicServer::new(ShadowQuicServerCfg {
        tag: "inbound".into(),
        bind_addr: address,
        users: vec![AuthUser {
            username: "user".into(),
            password: "pass".into(),
        }],
        jls_upstream: JlsUpstream {
            addr: "127.0.0.1:9".into(),
            ..Default::default()
        },
        alpn: vec!["h3".into()],
        zero_rtt: true,
        congestion_control: CongestionControl::Bbr,
        ..Default::default()
    })
    .await
    .expect("cannot create the server");
    let manager = Manager::single(Box::new(server), outbound);
    let serving = tokio::spawn(manager.run_until(async {
        let _ = tokio::task::spawn_blocking(|| {
            let mut rest = [0u8; 64];
            let mut input = std::io::stdin().lock();
            while matches!(input.read(&mut rest), Ok(read) if read > 0) {}
        })
        .await;
    }));
    println!("{}", format_args!("{{\"port\":{}}}", address.port()));
    let _ = serving.await;
}
