//! The client's UDP socket to a server, bound to the interface it leaves through.
//!
//! Windows refuses the IPv6 interface option on an adapter with IPv6 switched off (error
//! 10022), and a socket for IPv4 and IPv6 together needs it even for an IPv4 server. The
//! binding is injected here, so these cases run on every system and need no such adapter.

use std::io;
use std::sync::{Arc, Mutex};

use shadowquic::config::Interface;
use shadowquic::utils::socket_opt::UdpSocketFactory;

/// The kind of socket a binding attempt got.
#[derive(Debug, PartialEq)]
enum Attempt {
    BothFamilies,
    Ipv6Only,
    Ipv4,
}

/// The kind of a socket. A macro, because this crate does not name the socket's type.
macro_rules! attempt {
    ($socket:expr) => {{
        let socket = $socket;
        if socket.local_addr().unwrap().is_ipv4() {
            Attempt::Ipv4
        } else if socket.only_v6().unwrap() {
            Attempt::Ipv6Only
        } else {
            Attempt::BothFamilies
        }
    }};
}

fn factory(server: &str) -> UdpSocketFactory {
    UdpSocketFactory {
        addr: server.to_string(),
        interface: Some(Interface::Device("test-device".to_string())),
        fw_mark: None,
        protect_path: None,
        try_dual_stack: true,
        resolver: Arc::new(shadowquic::dns::Resolver {}),
    }
}

/// A device that refuses IPv6, as a Windows adapter with IPv6 switched off does.
fn without_ipv6(attempts: &Mutex<Vec<Attempt>>, device: &str, kind: Attempt) -> io::Result<()> {
    assert_eq!(device, "test-device");
    let refused = kind != Attempt::Ipv4;
    attempts.lock().unwrap().push(kind);
    if refused {
        Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "no IPv6 on this device",
        ))
    } else {
        Ok(())
    }
}

#[tokio::test]
async fn a_device_without_ipv6_gets_an_ipv4_socket_for_an_ipv4_server() {
    let attempts = Mutex::new(Vec::new());
    let socket = factory("192.0.2.1:443")
        .create_socket_with(|socket, device| without_ipv6(&attempts, device, attempt!(socket)))
        .await
        .expect("an IPv4 socket can be bound to the device");
    assert_eq!(attempt!(&socket), Attempt::Ipv4);
    assert_eq!(
        *attempts.lock().unwrap(),
        [Attempt::BothFamilies, Attempt::Ipv4]
    );
}

#[tokio::test]
async fn a_device_that_takes_both_families_keeps_the_socket_for_both() {
    let attempts = Mutex::new(Vec::new());
    let socket = factory("192.0.2.1:443")
        .create_socket_with(|socket, _| {
            attempts.lock().unwrap().push(attempt!(socket));
            Ok(())
        })
        .await
        .expect("the device takes the socket");
    assert_eq!(attempt!(&socket), Attempt::BothFamilies);
    assert_eq!(*attempts.lock().unwrap(), [Attempt::BothFamilies]);
}

#[tokio::test]
async fn an_ipv6_server_through_a_device_without_ipv6_is_an_error() {
    let attempts = Mutex::new(Vec::new());
    let error = factory("[2001:db8::1]:443")
        .create_socket_with(|socket, device| without_ipv6(&attempts, device, attempt!(socket)))
        .await
        .expect_err("no socket reaches an IPv6 server through the device");
    assert_eq!(error.to_string(), "no IPv6 on this device");
    // The second socket is for IPv6; whether it also takes IPv4 is the system's default.
    let attempts = attempts.lock().unwrap();
    assert_eq!(attempts.len(), 2, "{attempts:?}");
    assert_eq!(attempts[0], Attempt::BothFamilies);
    assert_ne!(attempts[1], Attempt::Ipv4);
}
