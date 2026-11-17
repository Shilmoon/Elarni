//! A ShadowQUIC server on 127.0.0.1 for tests of the client and of the core's outbound.
//!
//! Prints `{"port":N}` and serves the user `user` / `pass` under the server name
//! `localhost` until standard input ends. Requests leave directly, so a test reaches its
//! own loopback targets through it.

use std::io::Read;
use std::net::SocketAddr;
use std::sync::Arc;

use shadowquic::Manager;
use shadowquic::config::{AuthUser, CongestionControl, JlsUpstream, ShadowQuicServerCfg};
use shadowquic::direct::outbound::DirectOut;
use shadowquic::shadowquic::inbound::ShadowQuicServer;

#[tokio::main(flavor = "multi_thread", worker_threads = 2)]
async fn main() {
    let address: SocketAddr = std::net::UdpSocket::bind("127.0.0.1:0")
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
    let manager = Manager::single(Box::new(server), Arc::new(DirectOut::default()));
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
