//! The client carries TCP and UDP to a loopback ShadowQUIC server and stops cleanly.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{Ipv4Addr, SocketAddr, TcpListener, TcpStream, UdpSocket};
use std::process::{Child, Command, Stdio};
use std::time::Duration;

const TIMEOUT: Duration = Duration::from_secs(10);

/// The test server example, built by cargo next to the test binaries.
struct Server {
    child: Child,
    port: u16,
}

impl Server {
    fn start() -> Self {
        let mut child = Command::new(example_path("shadowquic-test-server"))
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .expect("cannot start the test server; build it with `cargo build --examples`");
        let port = read_port(child.stdout.take().unwrap());
        Self { child, port }
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        drop(self.child.stdin.take());
        let _ = self.child.wait();
    }
}

fn example_path(name: &str) -> std::path::PathBuf {
    // target/<profile>/deps/traffic-<hash> -> target/<profile>/examples/<name>
    let mut path = std::env::current_exe().unwrap();
    path.pop();
    path.pop();
    path.push("examples");
    path.push(format!("{name}{}", std::env::consts::EXE_SUFFIX));
    path
}

fn read_port(output: impl Read) -> u16 {
    let mut line = String::new();
    BufReader::new(output).read_line(&mut line).unwrap();
    let answer: serde_json::Value = serde_json::from_str(&line).expect(&line);
    answer["port"].as_u64().expect(&line) as u16
}

fn config(server_port: u16, change: impl FnOnce(&mut serde_json::Value)) -> Vec<u8> {
    let mut value = serde_json::json!({
        "server": format!("127.0.0.1:{server_port}"),
        "serverName": "localhost",
        "username": "user",
        "password": "pass",
        "socksUsername": "local",
        "socksPassword": "secret",
    });
    change(&mut value);
    serde_json::to_vec(&value).unwrap()
}

fn tcp_echo() -> u16 {
    let listener = TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
    let port = listener.local_addr().unwrap().port();
    std::thread::spawn(move || {
        for stream in listener.incoming().flatten() {
            std::thread::spawn(move || {
                let mut reader = stream.try_clone().unwrap();
                let mut writer = stream;
                let _ = std::io::copy(&mut reader, &mut writer);
            });
        }
    });
    port
}

fn udp_echo() -> u16 {
    let socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
    let port = socket.local_addr().unwrap().port();
    std::thread::spawn(move || {
        let mut packet = [0u8; 2048];
        while let Ok((length, from)) = socket.recv_from(&mut packet) {
            let _ = socket.send_to(&packet[..length], from);
        }
    });
    port
}

/// Opens a SOCKS5 session with the login and sends `command` for 127.0.0.1:`port`.
/// Returns the stream and the address in the reply.
fn socks(
    socks_port: u16,
    login: (&str, &str),
    command: u8,
    port: u16,
) -> std::io::Result<(TcpStream, SocketAddr)> {
    let mut stream = TcpStream::connect((Ipv4Addr::LOCALHOST, socks_port))?;
    stream.set_read_timeout(Some(TIMEOUT))?;
    stream.set_write_timeout(Some(TIMEOUT))?;
    stream.write_all(&[5, 1, 2])?;
    let mut reply = [0u8; 2];
    stream.read_exact(&mut reply)?;
    assert_eq!(reply, [5, 2], "the listener must ask for the login");
    let mut auth = vec![1, login.0.len() as u8];
    auth.extend_from_slice(login.0.as_bytes());
    auth.push(login.1.len() as u8);
    auth.extend_from_slice(login.1.as_bytes());
    stream.write_all(&auth)?;
    stream.read_exact(&mut reply)?;
    if reply[1] != 0 {
        return Err(std::io::Error::other("login refused"));
    }
    let mut request = vec![5, command, 0, 1, 127, 0, 0, 1];
    request.extend_from_slice(&port.to_be_bytes());
    stream.write_all(&request)?;
    let mut answer = [0u8; 10];
    stream.read_exact(&mut answer)?;
    assert_eq!(&answer[..2], [5, 0], "request refused");
    assert_eq!(answer[3], 1, "expected an IPv4 address in the reply");
    let address = SocketAddr::from((
        [answer[4], answer[5], answer[6], answer[7]],
        u16::from_be_bytes([answer[8], answer[9]]),
    ));
    Ok((stream, address))
}

fn tcp_round_trip(socks_port: u16, target: u16, payload: &[u8]) {
    let (mut stream, _) = socks(socks_port, ("local", "secret"), 1, target).unwrap();
    stream.write_all(payload).unwrap();
    let mut echoed = vec![0u8; payload.len()];
    stream.read_exact(&mut echoed).unwrap();
    assert!(echoed == payload, "the echoed bytes differ");
}

fn udp_round_trip(socks_port: u16, target: u16, payload: &[u8]) {
    let (_control, relay) = socks(socks_port, ("local", "secret"), 3, 0).unwrap();
    let socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
    socket
        .set_read_timeout(Some(Duration::from_secs(2)))
        .unwrap();
    let mut packet = vec![0, 0, 0, 1, 127, 0, 0, 1];
    packet.extend_from_slice(&target.to_be_bytes());
    packet.extend_from_slice(payload);
    let mut answer = [0u8; 4096];
    // The first datagrams may be sent before the QUIC connection exists.
    for _ in 0..5 {
        socket.send_to(&packet, relay).unwrap();
        if let Ok((length, _)) = socket.recv_from(&mut answer) {
            assert_eq!(&answer[..10], &packet[..10], "reply header");
            assert!(
                &answer[10..length] == payload,
                "the echoed datagram differs"
            );
            return;
        }
    }
    panic!("no UDP reply through the client");
}

#[test]
fn tcp_and_udp_reach_their_targets_through_the_server() {
    let server = Server::start();
    let (tcp, udp) = (tcp_echo(), udp_echo());
    let client = elarni_shadowquic::start(&config(server.port, |_| {})).unwrap();
    assert!(client.is_running());

    tcp_round_trip(client.port(), tcp, b"hello");
    let large: Vec<u8> = (0..2_000_000u32).map(|i| (i % 251) as u8).collect();
    tcp_round_trip(client.port(), tcp, &large);
    udp_round_trip(client.port(), udp, b"datagram");
    udp_round_trip(client.port(), udp, &[7u8; 1100]);

    assert!(
        socks(client.port(), ("local", "wrong"), 1, tcp).is_err(),
        "a wrong login was accepted"
    );
    let port = client.port();
    client.stop();
    assert!(
        TcpStream::connect((Ipv4Addr::LOCALHOST, port)).is_err(),
        "the listener is still open after stop"
    );
}

#[test]
fn udp_over_stream_and_other_options_work() {
    let server = Server::start();
    let (tcp, udp) = (tcp_echo(), udp_echo());
    let client = elarni_shadowquic::start(&config(server.port, |value| {
        value["udpOverStream"] = true.into();
        value["zeroRtt"] = false.into();
        value["congestionControl"] = "cubic".into();
        value["initialMtu"] = 1280.into();
        value["minMtu"] = 1280.into();
    }))
    .unwrap();
    tcp_round_trip(client.port(), tcp, b"hello");
    udp_round_trip(client.port(), udp, b"datagram");
}

#[test]
fn a_wrong_password_carries_nothing() {
    let server = Server::start();
    let tcp = tcp_echo();
    let client = elarni_shadowquic::start(&config(server.port, |value| {
        value["password"] = "wrong".into();
    }))
    .unwrap();
    let (mut stream, _) = socks(client.port(), ("local", "secret"), 1, tcp).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let _ = stream.write_all(b"hello");
    let mut echoed = [0u8; 5];
    assert!(
        stream.read_exact(&mut echoed).is_err(),
        "data passed with a wrong password"
    );
}

#[test]
fn two_clients_run_side_by_side_and_stop_independently() {
    let server = Server::start();
    let tcp = tcp_echo();
    let first = elarni_shadowquic::start(&config(server.port, |_| {})).unwrap();
    let second = elarni_shadowquic::start(&config(server.port, |_| {})).unwrap();
    assert_ne!(first.port(), second.port());
    tcp_round_trip(first.port(), tcp, b"one");
    first.stop();
    tcp_round_trip(second.port(), tcp, b"two");
}

/// The helper program answers with its port, serves, and exits when its input ends.
#[test]
fn the_program_serves_until_its_input_ends() {
    let server = Server::start();
    let tcp = tcp_echo();
    let mut child = Command::new(env!("CARGO_BIN_EXE_elarni-shadowquic"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut input = child.stdin.take().unwrap();
    input.write_all(&config(server.port, |_| {})).unwrap();
    input.write_all(b"\n").unwrap();
    input.flush().unwrap();
    let port = read_port(child.stdout.take().unwrap());
    tcp_round_trip(port, tcp, b"hello");
    drop(input);
    let status = child.wait().unwrap();
    assert!(status.success(), "{status}");
    assert!(TcpStream::connect((Ipv4Addr::LOCALHOST, port)).is_err());
}

#[test]
fn the_program_reports_a_bad_configuration() {
    let mut child = Command::new(env!("CARGO_BIN_EXE_elarni-shadowquic"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    child.stdin.take().unwrap().write_all(b"{}\n").unwrap();
    let mut answer = String::new();
    child
        .stdout
        .take()
        .unwrap()
        .read_to_string(&mut answer)
        .unwrap();
    assert!(!child.wait().unwrap().success());
    let answer: serde_json::Value = serde_json::from_str(&answer).expect(&answer);
    assert!(
        answer["error"]
            .as_str()
            .unwrap()
            .contains("invalid configuration"),
        "{answer}"
    );
}
