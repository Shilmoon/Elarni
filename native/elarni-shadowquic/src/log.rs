//! Forwards the client's warnings and errors to the program that embeds it.
//!
//! The client reports through `tracing`. Only warnings and errors are kept, as one
//! line each, and at most `LINES_PER_SECOND`: a dead server makes every request fail.

use std::fmt::Write;
use std::sync::OnceLock;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Instant;

use tracing::field::{Field, Visit};
use tracing::span::{Attributes, Id, Record};
use tracing::{Event, Level, Metadata, Subscriber};

const LINES_PER_SECOND: u64 = 20;

type Sink = Box<dyn Fn(&str) + Send + Sync>;

static SINK: OnceLock<Sink> = OnceLock::new();

/// Sends the client's warnings and errors to `sink`. Only the first call in a process
/// takes effect; it returns whether this call did.
pub fn set_log_sink(sink: impl Fn(&str) + Send + Sync + 'static) -> bool {
    SINK.set(Box::new(sink)).is_ok()
        && tracing::subscriber::set_global_default(Forwarder::default()).is_ok()
}

#[derive(Default)]
struct Forwarder {
    /// Second of the current window in the upper bits, lines written in it in the lower 16.
    window: AtomicU64,
}

impl Forwarder {
    fn allow(&self) -> bool {
        static START: OnceLock<Instant> = OnceLock::new();
        let second = START.get_or_init(Instant::now).elapsed().as_secs();
        let update = self
            .window
            .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |window| {
                let lines = if window >> 16 == second {
                    window & 0xffff
                } else {
                    0
                };
                (lines < LINES_PER_SECOND).then_some(second << 16 | (lines + 1))
            });
        update.is_ok()
    }
}

impl Subscriber for Forwarder {
    fn enabled(&self, metadata: &Metadata<'_>) -> bool {
        *metadata.level() <= Level::WARN
    }

    fn max_level_hint(&self) -> Option<tracing::level_filters::LevelFilter> {
        Some(tracing::level_filters::LevelFilter::WARN)
    }

    fn new_span(&self, _: &Attributes<'_>) -> Id {
        Id::from_u64(1)
    }

    fn record(&self, _: &Id, _: &Record<'_>) {}

    fn record_follows_from(&self, _: &Id, _: &Id) {}

    fn event(&self, event: &Event<'_>) {
        let Some(sink) = SINK.get() else {
            return;
        };
        if !self.allow() {
            return;
        }
        let mut line = Line(String::with_capacity(160));
        let _ = write!(line.0, "{}", event.metadata().level());
        event.record(&mut line);
        sink(&line.0);
    }

    fn enter(&self, _: &Id) {}

    fn exit(&self, _: &Id) {}
}

struct Line(String);

impl Visit for Line {
    fn record_debug(&mut self, field: &Field, value: &dyn std::fmt::Debug) {
        let _ = if field.name() == "message" {
            write!(self.0, " {value:?}")
        } else {
            write!(self.0, " {}={value:?}", field.name())
        };
    }

    fn record_str(&mut self, field: &Field, value: &str) {
        let _ = if field.name() == "message" {
            write!(self.0, " {value}")
        } else {
            write!(self.0, " {}={value}", field.name())
        };
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_burst_is_cut_to_the_limit_per_second() {
        let forwarder = Forwarder::default();
        let allowed = (0..1000).filter(|_| forwarder.allow()).count() as u64;
        // The burst may cross one second boundary.
        assert!(
            (LINES_PER_SECOND..=2 * LINES_PER_SECOND).contains(&allowed),
            "{allowed}"
        );
    }
}
