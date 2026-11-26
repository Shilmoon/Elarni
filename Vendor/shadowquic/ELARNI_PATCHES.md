# ShadowQUIC in Elarni

This directory is the `shadowquic` and `shadowquic-macros` crates of
https://github.com/spongebob888/shadowquic at v0.4.12 (commit
fe852e5cad08538385bfe450732aedabb88c2553, MIT, see `LICENSE`), with the changes below.
Elarni builds only the ShadowQUIC client from it; `native/elarni-shadowquic` wraps that
client for the Xray core (`Vendor/Xray-core/proxy/shadowquic`).

## Fixes on top of v0.4.12

The "Unreleased" section of `CHANGELOG.md` lists them: 0-RTT requests that the server
rejects are sent again, a ClientHello spanning several packets, address-family changes
of the server name, bounded UDP state, UDP over stream with many destinations, SOCKS
replies and malformed input, the handshake queue of the inbounds, and a device that
refuses a UDP socket for both address families. They change no wire format: a patched
client or server works with a stock v0.4.12 peer.

## Changes made for Elarni

- `shadowquic/Cargo.toml` and `shadowquic-macros/Cargo.toml` are written out instead of
  inheriting from the upstream workspace, and declare only what the ShadowQUIC client
  needs. The features for the TUN and transparent-proxy inbounds, the HTTP inbound, the
  DNS server, the Lua router, the REST interface, SunnyQUIC and jemalloc, with their
  dependencies, are not declared; their sources stay in `src/` unchanged and are compiled
  out. `[lints.rust]` allows the feature names the sources still mention.
  The REST interface (`src/api`) in particular cannot be switched on from this copy: neither
  its feature nor hyper is declared. It answers any origin and needs no secret by default, so
  do not declare it here without an origin check and a required secret.
- New feature `global-allocator` (on by default, off in Elarni): upstream always installs
  an allocation-counting global allocator; a library must leave that to its program
  (`src/lib.rs`).
- `SocksServer::bound_addr` publishes the address the SOCKS listener got, so that a
  program can bind port 0 and read the port (`src/socks/inbound.rs`).
- `UdpSocketFactory::create_socket_with` takes the call that binds a socket to a device, so
  that `native/elarni-shadowquic/tests/device_bind.rs` can refuse the binding of the socket
  for both families, as Windows does on an adapter with IPv6 switched off, and check that
  the client falls back to a socket of the server's family (`src/utils/socket_opt.rs`).
- On Windows a dual-stack socket whose IPv4 interface option (`IP_UNICAST_IF`) fails no
  longer counts as bound: its IPv4 traffic would leave by the default route. The bind
  fails and the fallback above opens an IPv4 socket (`src/utils/platform/windows.rs`).
- Left out: `src/main.rs` (the command-line program), `build.rs` (only for jemalloc on
  Android), `tests/`, `examples/`, `config_examples/` and the other crates and files of the
  upstream repository. The unit tests inside `src/` are unchanged; some read
  `config_examples/` and need the upstream tree to run, and the workspace runs none of
  them (cargo does not test a package outside the workspace that has dev-dependencies).
  The device fallback above is tested from `native/elarni-shadowquic` instead: an upstream
  sync that drops the fix fails that test.

## Tests

Elarni tests the client it builds: `cargo test -p elarni-shadowquic` (TCP and UDP through
a loopback ShadowQUIC server made from this source, in both UDP modes, and the UDP socket
bound to a device with an injected binding) and
`go test ./Vendor/Xray-core/proxy/shadowquic` (the same through a core). The full upstream
suite ran against this source, as a patch on the upstream tree, before it was copied.

## Updating

Take the new upstream `shadowquic/src` and `shadowquic-macros`, reapply the four source
changes above, compare the dependency versions in `shadowquic/Cargo.toml` with upstream's
workspace manifest, and run the two test commands.
