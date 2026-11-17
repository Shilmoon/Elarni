//! The ShadowQUIC client as a child process of a core that cannot link it.
//!
//! The first line on standard input is the JSON description. The answer is one line on
//! standard output: `{"port":N}` once the SOCKS5 listener on 127.0.0.1 accepts, or
//! `{"error":"..."}` followed by exit status 1. The client runs until standard input
//! ends, so it never outlives the core that started it. Warnings and errors go to
//! standard error, one per line.

use std::io::{Read, Write};
use std::process::ExitCode;
use std::sync::mpsc;
use std::time::Duration;

fn main() -> ExitCode {
    elarni_shadowquic::set_log_sink(|line| eprintln!("{line}"));
    let mut config = String::new();
    let started = match std::io::stdin().read_line(&mut config) {
        Ok(_) => elarni_shadowquic::start(config.as_bytes()),
        Err(error) => Err(format!("cannot read the configuration: {error}")),
    };
    let client = match started {
        Ok(client) => client,
        Err(error) => {
            answer(&serde_json::json!({ "error": error }));
            return ExitCode::FAILURE;
        }
    };
    answer(&serde_json::json!({ "port": client.port() }));

    let (ended, input_ended) = mpsc::channel::<()>();
    std::thread::spawn(move || {
        let mut rest = [0u8; 256];
        let mut input = std::io::stdin().lock();
        while matches!(input.read(&mut rest), Ok(read) if read > 0) {}
        let _ = ended.send(());
    });
    loop {
        match input_ended.recv_timeout(Duration::from_millis(500)) {
            Err(mpsc::RecvTimeoutError::Timeout) if client.is_running() => {}
            Err(mpsc::RecvTimeoutError::Timeout) => {
                eprintln!("ERROR the ShadowQUIC client ended by itself");
                return ExitCode::FAILURE;
            }
            _ => break,
        }
    }
    client.stop();
    ExitCode::SUCCESS
}

fn answer(value: &serde_json::Value) {
    let mut output = std::io::stdout().lock();
    let _ = writeln!(output, "{value}");
    let _ = output.flush();
}
