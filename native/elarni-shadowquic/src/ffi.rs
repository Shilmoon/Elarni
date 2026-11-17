//! C interface for a core that runs in the same process.

use std::collections::HashMap;
use std::ffi::c_char;
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};

use crate::Client;

static CLIENTS: Mutex<Option<HashMap<u64, Client>>> = Mutex::new(None);
static NEXT_HANDLE: AtomicU64 = AtomicU64::new(1);

/// Starts a client from `config_len` bytes of UTF-8 JSON at `config`.
///
/// Returns a handle other than 0 and stores the port of the SOCKS5 listener on
/// 127.0.0.1 in `port`. On failure returns 0 and, when `error` is not null, stores a
/// message of at most `error_len` bytes there, always ending with a zero byte.
///
/// # Safety
/// `config` must point to `config_len` readable bytes, `port` to a writable `uint16_t`
/// and `error`, unless null, to `error_len` writable bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn elarni_shadowquic_start(
    config: *const u8,
    config_len: usize,
    port: *mut u16,
    error: *mut c_char,
    error_len: usize,
) -> u64 {
    let result = if config.is_null() || port.is_null() {
        Err("missing argument".to_owned())
    } else {
        // SAFETY: the caller guarantees `config_len` readable bytes.
        let config = unsafe { std::slice::from_raw_parts(config, config_len) };
        catch_unwind(AssertUnwindSafe(|| crate::start(config)))
            .unwrap_or_else(|_| Err("the client failed to start".to_owned()))
    };
    match result {
        Ok(client) => {
            // SAFETY: checked above that `port` is not null; the caller guarantees it is writable.
            unsafe { port.write(client.port()) };
            let handle = NEXT_HANDLE.fetch_add(1, Ordering::Relaxed);
            lock().get_or_insert_default().insert(handle, client);
            handle
        }
        Err(message) => {
            // SAFETY: the caller guarantees `error_len` writable bytes unless `error` is null.
            unsafe { write_message(&message, error, error_len) };
            0
        }
    }
}

/// Stops the client behind `handle` and waits until it has ended. An unknown handle,
/// or one stopped before, is ignored.
#[unsafe(no_mangle)]
pub extern "C" fn elarni_shadowquic_stop(handle: u64) {
    let client = lock().as_mut().and_then(|clients| clients.remove(&handle));
    if let Some(client) = client {
        let _ = catch_unwind(AssertUnwindSafe(|| client.stop()));
    }
}

/// Whether the client behind `handle` is still running.
#[unsafe(no_mangle)]
pub extern "C" fn elarni_shadowquic_running(handle: u64) -> bool {
    lock()
        .as_ref()
        .and_then(|clients| clients.get(&handle))
        .is_some_and(Client::is_running)
}

/// Sends the clients' warnings and errors to `sink`, one line of `len` bytes of UTF-8
/// (not zero-terminated) per call, from any thread. Only the first call in a process
/// takes effect.
#[unsafe(no_mangle)]
pub extern "C" fn elarni_shadowquic_set_log(
    sink: Option<unsafe extern "C" fn(line: *const c_char, len: usize)>,
) {
    let Some(sink) = sink else {
        return;
    };
    crate::set_log_sink(move |line| {
        // SAFETY: the pointer and length describe `line`, which outlives the call.
        unsafe { sink(line.as_ptr().cast(), line.len()) }
    });
}

fn lock() -> std::sync::MutexGuard<'static, Option<HashMap<u64, Client>>> {
    CLIENTS
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

/// # Safety
/// `error`, unless null, must point to `error_len` writable bytes.
unsafe fn write_message(message: &str, error: *mut c_char, error_len: usize) {
    if error.is_null() || error_len == 0 {
        return;
    }
    let mut length = message.len().min(error_len - 1);
    while !message.is_char_boundary(length) {
        length -= 1;
    }
    // SAFETY: `length + 1 <= error_len` writable bytes.
    unsafe {
        std::ptr::copy_nonoverlapping(message.as_ptr(), error.cast::<u8>(), length);
        error.add(length).write(0);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn message_of(buffer: &[u8]) -> &str {
        let end = buffer
            .iter()
            .position(|byte| *byte == 0)
            .expect("no terminator");
        std::str::from_utf8(&buffer[..end]).unwrap()
    }

    #[test]
    fn a_failed_start_returns_no_handle_and_a_message() {
        let mut port = 7u16;
        let mut error = [0x55u8; 64];
        let config = b"{}";
        let handle = unsafe {
            elarni_shadowquic_start(
                config.as_ptr(),
                config.len(),
                &mut port,
                error.as_mut_ptr().cast(),
                error.len(),
            )
        };
        assert_eq!(handle, 0);
        assert_eq!(port, 7);
        assert!(message_of(&error).starts_with("invalid configuration"));
    }

    #[test]
    fn a_message_is_cut_at_a_character_boundary_and_terminated() {
        let mut error = [0x55u8; 6];
        unsafe { write_message("aaaaé", error.as_mut_ptr().cast(), 6) };
        // The two-byte "é" would end at byte 6, which the terminator needs.
        assert_eq!(message_of(&error), "aaaa");
        let mut one = [0x55u8; 1];
        unsafe { write_message("abc", one.as_mut_ptr().cast(), 1) };
        assert_eq!(one, [0]);
        unsafe { write_message("abc", std::ptr::null_mut(), 8) };
    }

    #[test]
    fn missing_arguments_and_unknown_handles_are_harmless() {
        let mut port = 0u16;
        let handle = unsafe {
            elarni_shadowquic_start(std::ptr::null(), 0, &mut port, std::ptr::null_mut(), 0)
        };
        assert_eq!(handle, 0);
        elarni_shadowquic_stop(0);
        elarni_shadowquic_stop(u64::MAX);
        assert!(!elarni_shadowquic_running(u64::MAX));
    }
}
