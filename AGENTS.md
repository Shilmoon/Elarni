# Elarni

A cross-platform routing client: a Flutter UI over a forked Xray core.
Human contributors: see [CONTRIBUTING.md](CONTRIBUTING.md); these notes are for coding agents.

## Toolchains

Versions are pinned in `mise.toml` and `rust-toolchain.toml`. Run `mise install` and
`rustup show` once; the mise shims directory must be on PATH.

## Checks

Run `python scripts/check-changed` after every change. It selects the checks your
uncommitted changes affect (on Linux, with the lint CI runs for Go and Rust), runs them and
prints the failing output; `--base <commit>` also covers committed work and `--all` runs
everything CI runs on this OS. It exits non-zero when a changed file has no local check
(`android/`, `.github/`, ...): only CI covers those. A `FLAKY` line means a check passed only
on its retry; report it (`python scripts/flaky-report --ci 40` ranks the tests that failed and
then passed, locally and in the last CI runs). Each run writes `.build/check-results/<UTC time>-<id>/summary.json`
and `output.log`. [docs/TESTING.md](docs/TESTING.md) lists each suite.

- Dart: analyze only the touched files (`flutter analyze --no-pub <files>`).
- Rust: `cargo fmt --all` and `cargo clippy --workspace --all-targets --locked -- -D warnings`.
- Go: `gofmt -l Vendor` must print nothing.
- Tests must not sleep through production timeouts; inject shorter ones
  (`DesktopProxy(replyTimeout:, helperExitGrace:, automationProbeSeconds:)` in Dart,
  `splithttp.SetSetupTimeout` in Go).
- Do not weaken assertions or skip failing tests to get a pass.

## Concurrency and remote runs

- Several agents may share one checkout. Check `git status`, `git log` and `git reflog` right
  before committing, and never switch branches or reset files you did not change. Work in a
  git worktree inside the session's folder (for example `claude -w`), one per task, removed
  when its pull request merges.
- `scripts/check-linux-container` (Linux CI in a warm Ubuntu 22.04 container, local Docker or
  `--host`) and `scripts/check-mac --host` (a kept workspace on a Mac) run one at a time per
  host: a second run is refused, not queued (only a CI job waits, up to 45 minutes, for a manual
  run on its runner's host). Start one in the background and wait for it to exit; do not poll
  with `sleep`.
- The CI runs on one self-hosted Linux runner and one Windows VM, so pull requests queue behind
  main. Wait for a run with `python scripts/ci-wait [run-id]` in the background, not with
  repeated `gh run list` or `gh run view`: it exits when the run ends and prints each job's
  result and the end of every failed job's log.

## Changes

- Keep a change to one purpose; follow a changed interface through every platform that uses it.
- Conventional Commits; add a line under "Unreleased" in `CHANGELOG.md`.
- GitHub release notes are short and written for users, not a copy of `CHANGELOG.md`: follow
  "Release notes" in [docs/RELEASING.md](docs/RELEASING.md).
- Never commit keys, signing material, real server addresses or subscription URLs; tests use
  loopback fixtures.
- Pull requests state the AI tool used and what it did (see CONTRIBUTING.md).
