# Changelog

Notable changes to Elarni. Versions follow `pubspec.yaml`.

## Unreleased

### Changed

- For contributors: the Supply chain checks run on a pull request only when it changes the locked
  dependencies, the Go version or the Gradle wrapper, and daily on `main` instead of weekly. A
  newly published advisory used to fail every open pull request, whatever it changed.
- For contributors: a CI job that finds the Linux container in use by a manual
  `scripts/check-linux-container` run waits for it to end, up to 45 minutes, instead of failing
  within seconds. Such a failed job could only be rerun together with its whole workflow run.
- For contributors: an Android acceptance run (`scripts/check-android`) no longer fails for a
  test that failed in its suite and passed when rerun alone. The test is still reported as
  `FLAKY`, the CI run's summary names it, and `scripts/flaky-report` ranks it. A test that fails
  again, a crash or a suite that ran too few tests fails the run as before.
- For contributors: `main` does not repeat the Android emulator acceptance (about 20 minutes on
  the one Linux runner) for files that already passed it in a manually started run of the
  branch. The runner keeps a record per tree.
- For contributors: the Swift checks run in the macOS 26 job beside the iPhone build instead of
  in the macOS 15 checks job, which took 18 minutes and ended every pull request's run last. Each
  macOS job now takes about 12 minutes, and no check was dropped.
- For contributors: a pull request that changes only one platform's own folder (`android/`,
  `ios/`, `macos/`, `windows/`, `linux/`) runs only the CI jobs it can affect, so an Android or
  iPhone change no longer waits for the Windows VM. `main` still runs every job.
- For contributors: `python scripts/ci-wait` no longer waits for days on a run whose runner is
  down. When a job stays queued for a self-hosted runner that is offline, it names the job and
  the runner and stops with exit code 3. It also no longer reports a run as not finished when
  GitHub fails to answer one request: it goes on waiting.
- For contributors: the Windows CI job frees disk space on its VM before it builds
  (`scripts/free-runner-disk`). With less than 8 GB free it removes old temporary files, Go
  build cache entries unused for 2 days and the Rust incremental caches, and only then the Rust
  build outputs. The VM's disk had filled up and failed the job.

### Fixed

- For contributors: the path-change burst tests of Hysteria, gRPC and TUIC
  (`TestBurstOfPathChangesRetiresOnceMore`) no longer fail on a runner whose timers run late.
  They sent ten signals over 90% of one check whatever the time taken, and a signal sent after
  the check rightly started a third pass; the burst now ends by the clock, as WireGuard's does.
- For contributors: the required "checks passed" check no longer passes when GitHub cancelled a
  job it found no runner for. It now reads each job's own result, because GitHub reports such a
  job matrix as passed.
- For contributors: two Android emulator tests no longer fail by timing. "Disconnect before the
  service starts" waited for the VPN notification to go and then read it again, while Android
  shows that notification for about a millisecond at a moment of its own; it now waits for one
  state, the service gone and no notification. The Wi-Fi tests join the emulator's network by
  name after airplane mode instead of waiting for Android to do it (Android 11 held back for
  minutes).
- For contributors: a TUIC emulator test that fails now says when its request began and how far
  it got, and `check-android` prints the TUIC peer's whole log, with times. The reply that came
  back empty six times in CI is not explained yet.
- For contributors: the DNS test "an empty AAAA answer ends a parallel lookup" no longer fails
  the race check (it did in two of nine Linux runs on 2026-11-26). The delay before a UDP query
  is sent again was a package variable that tests changed and restored while a query of the
  finished lookup was still being sent; it now belongs to each name server.
- For contributors: the TUIC test "stalled downloads leave the connection usable" no longer fails on a
  slow runner (four Windows runs from 2026-11-18 to 11-26, each with "a fresh download got 348920
  bytes"). It waited one second for twelve stalled downloads to fill and then gave a fresh one five
  seconds for a megabyte; when the link was slow, the twelve were still filling and took most of it.
  The test now waits until the server has sent all the stalled downloads may hold, and stops when
  the server sends nothing for ten seconds.

## 0.6.5 - 2026-11-26

### Added

- **Windows and Linux: start at login hidden in the tray.** Under "Launch app at login", "Start
  hidden in the tray" starts Elarni at login with only its tray icon; the tray icon, opening
  Elarni again or a link shows the window. On Linux, when no tray has shown the icon within
  20 seconds of the login, the window starts minimized instead.
- **Linux: a server keeps answering incoming connections while the VPN is on.** With the new
  `answerInbound` setting (`elarni-cli settings`, off by default), a connection that reaches the
  machine from outside (SSH, a web server, a published container port) is answered through the
  network it came from instead of the tunnel, and sessions opened before the tunnel keep working.
  A UDP service is answered outside only when it replies from the address the request reached
  (bound to that address, or per packet as dnsmasq and unbound do with interface binding). Under
  the kill switch the machine's own services answer; ports forwarded to containers stay blocked.
  What the machine opens itself, and its answers to what came in through the tunnel, still go
  through the VPN. `linux/HEADLESS.md` has the command.

### Fixed

- **Windows: "Block traffic without VPN" now also holds WSL2, Hyper-V and Mobile Hotspot traffic.**
  What this PC passed on for them left outside the VPN for a few seconds while Elarni switched
  servers or after the tunnel was lost; it is now dropped until the tunnel is back.
- **Hysteria2, TUIC and XHTTP over HTTP/3 servers given by name try the name's other
  addresses.** When the first address did not answer, for example an IPv6 address on a network
  without IPv6, the connection waited for it until the time ran out. Now the next of up to four
  addresses starts when one fails or after 2 seconds, while the earlier ones keep waiting, and
  the first that answers is used and tried first next time, until the network changes.
- **Linux: the AppImage starts on Debian 13, Fedora, Arch and openSUSE.** It stopped at once
  on systems without the ayatana tray libraries or GStreamer. The AppImage now carries the
  tray libraries (their source code is a
  separate download of each release), and without GStreamer only scanning QR codes with a
  camera is unavailable. The AppImage and the portable archive need glibc 2.34 or newer.
- **A server whose network cuts transfers off no longer looks healthy.** Some hosting networks
  stop each connection after its first 16 KB or so: the quick server checks passed while pages
  never loaded. Elarni now also downloads about 100 KB through the connected server after
  connecting, every 15 minutes while you use it or after a download stalled, and when it stops
  passing traffic. When two
  such downloads in a row stall, the app shows "Connected, but this server's network cuts
  transfers off", and automatic recovery and "lowest latency" move to another server; a backup
  whose downloads stall is skipped. With no other server to move to, recovery reconnects the
  stalled one once and then keeps it until a download passes or the network changes.
- **Servers keep favorites, names and the selection when a 3x-ui panel rotates REALITY
  values.** A refresh in which the panel changed a server's remark (the traffic left) together
  with its REALITY short ID or SNI made every server new: favorites, renamed and hidden servers,
  the automation list and the selected server were lost. Such a server is now found again by
  its address, credentials and keys, on every platform; a server the panel lists more than once
  is told apart by a name of its own, otherwise by its place in the previous list.
- **A provider's messages are no longer servers.** Some providers put the traffic left, a device
  limit or "Package Ended" into the name of a line that points nowhere (a local or documentation
  address, a public DNS resolver, port 0 or 1). From a subscription's own public address these
  lines now show as "Provider: ..." under the subscription's name instead of as servers that
  fail every test, and a subscription that sends only such lines is added without an error and
  shows just those lines. Every platform applies the same rule, including Android for addresses
  with `_` in the host name.
- **Android: "Connect after device restart" waits for the network.** A server given by name
  was looked up once as the phone started, often before Wi-Fi or mobile data worked; the
  start failed and, with "Reconnect automatically" off, the phone stayed without VPN. A start
  at boot or by Always-on VPN that fails because the name has no answer or no network is up
  now tries again after 2, 4, 8 and 16 s, then every 30 s and at once when a network comes,
  for 2 minutes. Other failures, such as a refused setting, still end the start at once.
- **Android: per-app routing on Xiaomi (HyperOS, MIUI) and Honor phones.** Their systems show
  an app no other installed apps until their own permission allows it, so the list said "No
  other installed apps." Elarni now declares that permission, so its app settings can allow
  it. While it is denied, the list says so with a button to those settings, also when the
  system still shows some of its own apps, and the list is read again on return.
- **Android: no crash where Android has no file picker or settings screen.** Opening or saving
  a file on a device without a document picker (some TV boxes), and opening Android's VPN,
  location or app settings, the share sheet or the VPN permission dialog where the system has
  none, closed the app. Elarni now says that the device has no app or screen for it.
- **Android: a Keystore that fails for a moment offers Retry before a reset.** Any failure to
  decrypt the saved data read as damaged data, and the offered reset deletes the
  subscriptions. A failing Keystore now keeps the data and offers **Retry**, which opens it
  again and then makes the connection the failure stopped: at boot, by Always-on VPN, or one
  Android ended along with the app. For a fault that does not clear, Troubleshooting keeps
  **Reset Elarni** below Retry. A file that does not match its key, or a key Android removed,
  offers the reset as before.
- **REALITY with a randomized fingerprint could not connect on about half of the app starts.**
  The randomized hello carried the post-quantum key share that REALITY servers since Xray 26.9.8
  require only half the time; it now takes the hello that "random" picks, which always has it.
  With `randomizednoalpn` that hello carries no ALPN, and the log names the hello once.
- **The "Firefox" fingerprint failed with "invalid server key share" against servers that pick
  the P-256 key exchange**; uTLS now keeps a key for each share the hello offers.
- **AmneziaWG no longer loses a handshake retry or a keepalive timer change.** It moves to
  amneziawg-go v3.1.20261116, and a profile that names Jmin without Jmax still connects: it now
  imports, also with junk packets (Jc above 0), and is stored with Jmax equal to Jmin.
  WireGuard and AmneziaWG take gVisor's fix for TCP connections that retried a busy connection
  without yielding the processor.
- **The tunnel could stop carrying traffic after one read error of its network adapter**, while
  the app still showed it connected. Elarni reads on after such an error and stops reading only
  when the tunnel stops or its adapter is gone. An error that repeats is logged once a minute,
  with how many times it came.
- **WireGuard could keep a CPU core busy while its socket kept failing.** It now waits longer
  after each error, up to a second, instead of retrying at once. An error that only reports a
  packet the server did not take, as while it is briefly down, does not delay its next answer.
- **Hysteria2 uses the upload speed you set, as the official client does.** Brutal congestion
  control starts whenever the up speed is set, capped by the server's limit if it has one; it
  needed a server limit too. "100 mbps" now means 100,000,000 bits per second, not 104,857,600.
  For server operators: a Hysteria2 inbound of this core without `brutalUp` now sends to each
  client with Brutal at the download speed that client asks for, with no limit, as official
  Hysteria does without `ignoreClientBandwidth`; set `brutalUp` to cap it, or
  `"congestion": "bbr"` to keep BBR.
- **A Hysteria2, MASQUE, XDRIVE or HTTP/3 XHTTP server, or an XDNS resolver over DNS over
  HTTP/3, chained through another one could freeze when a connection closed.** The socket under
  it is closed first now, so the close no longer waits for the outer connection to end. A failed
  XDNS connection over HTTP/3 no longer leaves its socket open.
- **An XHTTP `scMaxEachPostBytes` of 0 or less, such as `"0-1000000"`, crashed the core** at
  the first connection. The profile is refused with an error that names the setting.
- **Mux uses its configured concurrency again (8 by default).** Every outbound had been raised
  to 128 connections per mux; only XDRIVE, which needs it, still is.
- **Server tests left a connection open for every server that accepted but stalled in TLS.**
  The test's own timeout now ends the TLS handshake and the wait for an answer too.
- **XDNS with a TCP resolver did not work**: its queries went out without the length that DNS
  over TCP needs. The resolver now sends and answers them on a pool of connections; a query left
  without an answer gives its connection up after 5 s.
- **DNS answers over TCP could arrive broken when several were sent at once**, and an answer read
  while the DNS cache was cleaned could lose its addresses.
- **Connections of a stopped tunnel no longer run on in the background**: stopping the tunnel
  cancels the connections it handed to the core.
- **Ranges in profiles now include their upper end.** Fragment, noise, padding and XHTTP size
  ranges never took their largest value, so "1-2" was always 1.
- **A Hysteria2, TUIC, XHTTP/3, MASQUE, XDRIVE or DNS over HTTP/3 connection no longer closes
  over one packet it could not send.** A full send buffer, frequent on Mac and iPhone, closed any
  of these but TUIC, and a moment without a network, route or local address while the device
  moved between Wi-Fi and mobile data closed all of them, so every app on them reconnected. Such
  a packet is now resent like a lost one. When sends keep failing for 5 seconds, the connection
  closes as before, so apps reconnect instead of waiting for the idle timeout; a connection
  attempt to a server the device has no route to (an IPv6 address without IPv6) still fails at
  once. All platforms.
- **A long-lived Hysteria2, TUIC, XHTTP/3, MASQUE or XDRIVE connection no longer runs out of
  streams.** A stream
  that either side stopped while an app was still sending on it never gave its slot back, so
  after many such streams (a video or download closed mid-transfer) the connection refused new
  ones with "too many open streams". All platforms.
- **Servers with a long certificate chain or a post-quantum key exchange connect over
  Hysteria2, TUIC, XHTTP/3, MASQUE, XDRIVE and DNS over HTTP/3.** A server's first handshake reply larger than 16 KiB failed the
  connection with CRYPTO_BUFFER_EXCEEDED. All platforms.
- **A UDP packet over Hysteria2, TUIC or MASQUE is no longer delivered twice on a path that
  reorders packets.** After more than 64 gaps in what had arrived, a late copy of an old packet was taken
  as new. All platforms.
- **Hysteria2, TUIC, XHTTP/3, XDRIVE and DNS over HTTP/3 connect on networks that do not take
  1,280-byte packets** (some tunnels and mobile networks): the connection attempt ran into its
  timeout and now falls back to 1,200-byte packets. On these connections and MASQUE, game and
  call traffic no longer delays flow-control updates and resent data, UDP packets are no longer
  dropped while lost data is resent, and sending no longer pauses when the send buffer frees up
  at the wrong moment. TUIC with BBR takes SagerNet's changes to how periods with nothing to
  send reach BBR. All platforms.
- **WireGuard drops a malformed IPv6 packet instead of passing on part of it**: a packet whose
  length field was near 65,535 bytes went to the tunnel cut short. A handshake reply that
  arrived twice at once while a new handshake started could also set up keys of the old
  handshake. All platforms.
- **A DNS resolver running on the computer works in VPN mode.** On Windows the DNS guard blocked
  every lookup to port 53 that did not go through the tunnel, including lookups to 127.0.0.1
  and ::1, so apps that used a local resolver (dnscrypt-proxy, Acrylic, AdGuard Home) got no
  answers. On Linux such lookups went to Elarni's DNS instead of the resolver. Lookups to
  loopback now reach the resolver. Its own plain lookups to its upstream servers still go
  through the tunnel (Linux) or are blocked (Windows), as are lookups to the router or another
  resolver on the local network. On Linux, systemd-resolved (127.0.0.53, 127.0.0.54) is still
  answered by Elarni, because its own lookups would leave outside the tunnel.
- **Linux: the VPN puts its firewall rules back when another program removes them.** A program
  that ran `nft flush ruleset` removed Elarni's DNS redirect and kill switch rules, so DNS
  lookups went around the tunnel until the next Connect. Elarni checks once a second, also for a
  flushed table or a removed chain, puts the rules back and logs it. If it cannot put them back,
  it logs why and the VPN disconnects.
- **Windows: a Wintun failure says what went wrong.** When the Windows network driver (Wintun)
  could not be loaded or could not create its network adapter, Elarni showed a generic
  connection failure and switched to backup servers, which failed the same way. The message now
  names the driver, says that antivirus software or a device installation policy can cause this,
  and suggests reinstalling Elarni or using System proxy mode; no backup server is tried. The
  diagnostics export keeps the Windows error code.
- **Not now on an update keeps that version from installing by itself.** With automatic updates
  on, a release you put off with **Not now** was still downloaded and installed at the next
  check or start. Now it waits until you press **Update**. A newer release is offered and
  downloaded as usual.
- **More panel and app links import.** Hysteria2 links with `obfs=salamander` next to a
  FinalMask (`fm`) that says the same, `mports` port hopping and an old `fm` with
  `quicParams.udpHop`; Hysteria2 Gecko without packet sizes (Hysteria's 512 and 1200), in links
  and Clash profiles; Shadowsocks with `plugin=obfs-local;obfs=http` (simple-obfs HTTP mode;
  client-only options such as `fast-open` are left out) and Clash `plugin: obfs`;
  WireGuard links with a `/` before the query, `privatekey`, a UDP `fm` and `ifp=0`, and keys
  written with a bare `+` (also in an `ech` config list); WireGuard configurations with an IPv6
  `Endpoint` written without brackets; TUIC links with `congestion_control=BBR`, `reduce_rtt`
  or `fp`; versioned fingerprints such as `fp=chrome_120` (the browser's preset); mKCP `mtu` and
  `tti`, which a shared link now keeps; VMess QR JSON with `"tls":"reality"`. What still cannot work says why: simple-obfs TLS mode,
  a link whose `obfs` says Salamander and its `fm` Gecko (or the other way round), WireGuard
  noise packets (`ifp`), TUIC `disable_sni=1`, VMess REALITY without a public key.
- **Server names no longer end in `?serverDescription=…`.** 3x-ui appends a server's
  description to its name this way for Happ; Elarni leaves it out in every link and profile
  format.
- **A subscription at a panel's root named in its link, `https://host/#Name`, is added under
  that name** on every platform, instead of failing or importing as an HTTP proxy. An
  `http://` link at a root path stays an HTTP proxy, as before.
- **A subscription encrypted for Happ says so.** A feed that only Happ can decrypt (an
  `Encrypt-Tag` header and `?key=` in its link) failed as an invalid or unsupported feed; the
  message now says it is encrypted for another app and to ask the provider for another link.
- **WireGuard and AmneziaWG carry traffic again right after the device wakes**, instead of
  after about 15 s: after a sleep of a minute or more since the last key exchange, the next
  packet starts a new one. A tunnel that sends nothing does nothing on waking.
  Names with many addresses resolve again in apps that take small DNS answers or ask over TCP,
  a name without an IPv6 address no longer waits for the next DNS server, and on the Mac and
  iPhone a probe to the tunnel's own addresses ends at once instead of hanging until its timeout.
- **iPhone: encryption uses the phone's AES and SHA instructions.** The iPhone core ran
  AES-GCM and SHA-256 without them, costing battery and heat on every connection that uses
  them. In the iPhone simulator, AES-128-GCM went from 146 to 9,115 MB/s and SHA-256 from 458
  to 3,247 MB/s; the phone itself was not measured. A Release build also compiles its packet
  path and ShadowQUIC client with full optimization.
- **Windows: Docker in WSL can create networks while Elarni is connected.** With WSL in mirrored
  networking mode, `docker network create` and `docker compose up` failed with "all predefined
  address pools have been fully subnetted". The tunnel's routes had no next hop, WSL showed them
  to Linux as directly connected networks, and Docker gives no network a subnet inside one. The
  routes now go through an address of the tunnel adapter's own subnet, which routes the same
  traffic and leaves Docker's address pools free.
- **Windows: ShadowQUIC did not connect on a network adapter with IPv6 switched off.** With
  "Internet Protocol Version 6 (TCP/IPv6)" unticked in the adapter's properties, every
  connection through a ShadowQUIC server failed at once, and the log repeated "bind socket to
  interface (IPv6 Unicast) failed ... os error 10022". The client opened its socket for IPv4
  and IPv6 together and Windows refused to bind it to an adapter without IPv6, although the
  server address was IPv4. When the adapter refuses that socket, the client now opens one for
  the server's address family alone. The log still shows the refused IPv6 binding, followed by
  a line saying that the client uses a socket for IPv4 only. Since 0.6.2.
- **A server without IPv6 no longer gets apps' IPv6 traffic through DNS answers.** With the IP
  type "Automatic" (the default for subscription profiles) Elarni's DNS handed apps IPv6
  addresses whatever the server could reach. A server without IPv6 resets such TCP connections
  a round trip later and drops such UDP without a word, so an app with no IPv4 fallback waited
  out its own timeout, 5 s for QUIC. Elarni now finds out whether the server reaches public
  IPv6 (a TLS hello to Cloudflare's and Google's resolvers over IPv6, and over IPv4 to tell a
  missing IPv6 from a tunnel that is down), on the first IPv6 answer, again when the result is
  old, and on Android, the iPhone and the Mac after a change of network. While the server has no
  IPv6, IPv6 answers for names that go through it hold no public address and apps use IPv4;
  names that routing sends elsewhere (direct or to another server), LAN addresses, addresses
  set in hosts, and the IP types "IPv4" and "IPv6" are not affected. This also applies on
  Windows and Linux in TUN mode. Apps that resolve names themselves (DNS over HTTPS in a
  browser) or dial IPv6 addresses directly still reach the server with IPv6.

### Security

- **Two dependencies with published security advisories are updated**: the packet parser of
  the tunnel stack (etherparse 0.20.4, RUSTSEC-2026-0596) and the WebSocket library
  (gorilla/websocket, GO-2026-6278: the mask key now comes from the system's random source).
  No release of the WebSocket library has the fix yet, so Elarni uses the upstream commit that
  has it, with the library's other changes since v1.5.3; among them, a WebSocket inbound
  answers a request that is not a WebSocket upgrade with "426 Upgrade Required" instead of
  "400 Bad Request". All platforms.

## 0.6.4 - 2026-11-23

### Added

- **Desktop: a per-app rule shows what else it matches, and can keep to one version.** A rule
  follows its app through updates, so with two versions in use at once, such as Python 3.12
  and 3.13, one rule takes both. Its row now says "Also matches `Python\3.13\python.exe`"
  while programs run from both, with **Only this version** beside it. **Details** has
  **Follow updates** for a rule that follows, with the paths it matches now: turned off, the
  rule is the one path that is there (`exact:<path>`).

### Changed

- **A quiet connection is kept for 30 minutes instead of 5 on a new installation.** Elarni ended
  a connection with no data for the idle timeout, 300 seconds by default, so an idle SSH
  session dropped and push, mail and messenger connections reconnected every five minutes.
  The default is 1800 seconds; an installation that already saved its settings keeps its value
  (Settings, Advanced, "Connection idle timeout"). On iPhone and Mac a connection without data
  still ends after 10 minutes.
- **Help texts say what a mode does not cover.** System proxy mode: only the apps that follow
  the system proxy go through Elarni; games, calls and other UDP traffic connect directly.
  iPhone kill switch: iOS itself leaves push notifications and iMessage, carrier
  services (Wi-Fi Calling, MMS) and local-network devices (AirPlay, CarPlay) outside the tunnel.
  Mac kill switch: devices on the local network may not be found or reached, and macOS (from
  13.3) leaves push notifications and iMessage outside. Mac "Enforce tunnel routes": an included route that
  contains the local network takes its devices into the tunnel.

### Fixed

- **Telegram stuck on "Connecting" or "Updating", media that never loads.** Telegram disguises
  some connections to its data centers and to MTProto proxies as TLS to an unrelated site.
  Since 0.5.0 Elarni handed the server the site name it saw in such a connection instead of
  the address Telegram dialed, so the server connected to that site and Telegram kept
  retrying. The server now gets the name only when Elarni's DNS gave the app that very address
  for that name, for as long as that answer lives and at least an hour; otherwise it gets the
  address the app dialed. The same rule now guards a direct IPv6 connection that fails and is
  retried over IPv4 by name. So a connection whose first bytes name another host (a disguised
  one, or a request to an app's own HTTP proxy) reaches the address the app dialed. A name that
  did not come through Elarni's DNS is not handed over: an app with its own DNS over HTTPS,
  Android's Private DNS, a program that resolves names itself before it uses the proxy port,
  and for a while any app after Elarni's core process restarts. Behind a server without IPv6 the IPv6
  connections of those fail, as before 0.5.0. All platforms.
- **A site was routed by the addresses of the name in its first bytes, not by where it
  connects.** With the "IP if no domain matched" strategy (Happ's default), a VPN-mode connection
  that matched no rule had that name looked up and the answers tried against the IP lists. A
  connection disguised as TLS to another site (a Telegram proxy with a Russian cover name) then
  took that site's route, for example Direct by `geoip:ru`. The address the app dialed is used
  now and the name is not looked up, which also removes one DNS query per such connection.
- **A domain list now wins over an IP list of another action under "IP if no domain matched".**
  In VPN mode the lists were read in the route order, each action's sites and then its
  addresses, so an address list of an earlier action took a site listed for a later one: a
  Russian service on an address inside a "blocked" range was proxied, and a video cache on a
  Russian address went direct although its site was listed for the proxy. Sites are matched
  first and addresses second, as with FakeDNS and in proxy mode. A connection to a private
  address keeps the written order, and a rule that reads no name (a catch-all, a port or an
  app rule) still waits for the address rules above it. The name is the one in the
  connection's first bytes, so an address list that blocks does not stop a connection that
  names a site listed for another action, as it does not in proxy mode. "As is" and "IP on
  demand" are unchanged.
- **Sites with a long TLS hello missed their domain rule.** A hello with post-quantum keys
  that arrived in three or more pieces was not read, so the connection was routed by address.
  It is waited for within the same 200 ms.
- **HTTP/3 stalled after a pause on a site proxied by name.** A QUIC connection idle for longer
  than the UDP flow (2 minutes) continued in a new flow that carries no name, so it left
  through the route of its address. The flow keeps the name of the connection between the
  same two ports for 10 minutes. Android, Windows, Linux.
- **Proxy mode: a request was routed by the name inside it instead of the host it asked the
  proxy for.** The CONNECT or SOCKS host decides now.
- **Android: no progress line and Cancel at the bottom while subscriptions update.** A line
  with Cancel came up over the last servers on Home and Subscriptions, once for each
  subscription, while the spinner at the top showed the same update. The update now shows
  at the top alone: "Updating subscriptions…" with its Cancel, on Home and on Subscriptions.
  Cancel ends the whole update; it used to end only the download it interrupted and go on to
  the next subscription. While an update runs, Connect waits for it instead of turning into
  Stop. For other operations the line at the bottom has its own background, so the page under
  it no longer shows through.
- **ShadowQUIC: every open connection dropped after 30 seconds of silence.** The client sent no
  keep-alive, so the server connection timed out as soon as nothing was talking, and a quiet SSH
  session or push connection went with it. It now pings the server every 20 seconds, as the
  Hysteria2 and TUIC clients do.
- **AmneziaWG: large transfers hung while small requests worked** with a profile that sets `S4`
  and no `MTU`. The padding in front of every packet did not fit beside a full-size packet;
  the tunnel's default MTU is now 1420 minus `S4`, not below 1280.
- **UDP that only sends was cut every minute** on iPhone, Mac and in proxy mode: a flow was kept
  alive by replies only, so an upload or a log stream with no answers continued from a new
  port each minute.
- **A routing profile with a value in another letter case, or with a DNS type Elarni does not
  have, was refused as invalid.** `RouteOrder`, `DomainStrategy` and the DNS types are read in
  any case (`BLOCK_DIRECT_PROXY`, `doh`), and `udp` means DoU. A side whose DNS type is
  something else (DoT, DoQ, TCP) uses the default resolver of that side, as a profile without
  DNS settings does.
- **A routing profile with FakeDNS: Windows showed "No internet", and names of the local network
  went to the server.** FakeDNS answered every name with an address from 198.18.0.0/15. Windows
  compares the answer for `dns.msftncsi.com` with a fixed address, so it reported no internet and
  Office and Store sign-in failed. The Windows connectivity names, names of the user's own
  machine and LAN (`.lan`, `.local`, `.home.arpa`, `.internal`, a name without a dot, a local
  helper's `localhost.` name) and the carrier's Wi-Fi calling gateway now go to the profile's
  resolver; a name it does not know is "not found", so the system can still ask the LAN.
- **FakeDNS: an app that sends UDP from one socket to several names reached only the first**
  (a STUN check of several servers, a game's region probes) on Android, Windows and Linux. The
  later names' packets were sent to their 198.18.x.x address. Each name is now its own flow.
- **Custom "Included routes" with FakeDNS sent every connection by name outside the tunnel**,
  where nothing answers. A custom list now always carries 198.18.0.0/15 (and `fc00::/18` when
  it routes IPv6), and on Android, iPhone and Mac the tunnel's own DNS address.
- **iPhone, Mac: a server that speaks first no longer hangs now and then.** When the first bytes
  of a connection (the greeting of SSH, SMTP, FTP or a database) arrived together with the local
  proxy's reply, they were dropped and the app waited for a greeting that never came.
- **iPhone, Mac: ping is answered.** A ping through the VPN got no reply, so network tools and
  apps that check the connection with one reported "offline". It is answered by Elarni itself
  for every address, as on Android, Windows and Linux; it still does not measure the remote host.
- **Windows, macOS, Linux: per-app rules follow an app through its updates, whatever
  installed it.** An update moves many apps to a new path, and a rule for the old path stopped
  matching, one installer layout after another: Elarni knew Squirrel apps, Store apps and
  folders of versions, and only for apps picked from the app list. Paths are now compared
  without what names a build, in every rule and every running program: numbers joined by dots
  (`app-1.0.9265`, `Application\131.0.2903.86`, `jdk-21.0.2`), a build number that ends a
  folder's name after a dot, dash or underscore (`Battle.net.15173`), a folder named by a
  build's hash (Roblox, Visual Studio Code), a Store app's package folder, a Snap's revision
  and a Nix store path's hash. A number that is part of a name (`Photoshop 2025`, `jdk-21`,
  `Python312`) is followed another way, so that two products installed side by side, such as
  `Anno 1800` and `Anno 2070`, stay two apps: a rule that names nothing on the disk anymore,
  in any build, matches the program in the folder that replaced its own. Rules saved earlier
  match again without a change, however they were added, and `exact:<path>` names one path as
  it is written. See "Rules and app updates" in
  [docs/PER_APP_ROUTING.md](docs/PER_APP_ROUTING.md).
- **Desktop: the per-app app list shows an app as listed when a rule saved for another
  version of it routes it.** The editor compared the rules' text while routing compared apps,
  so after an update a routed app showed as not listed, ticking it added a second rule that
  the first one overruled, and the saved rule lost the app's name and icon. An app listed with
  the programs it starts showed as not listed too. The core now tells the editor which saved
  rule each app is.
- **Windows: a Microsoft Store app's rule takes the app's processes that run from Windows's
  own folder.** Some Store apps run inside `WWAHost.exe`, and their background tasks inside
  `backgroundTaskHost.exe`; a rule for the app's package did not match them. Windows is now
  asked which package a process runs as, and **Recent connections** names such a process by
  its app, and adds the app's rule, not the host's path.
- **macOS: a rule for an app that macOS runs from a temporary copy matches it.** An app
  opened where it was downloaded, or from its disk image, runs from a folder with a new name
  at each start (App Translocation); its rule, and one saved from **Recent connections**,
  stopped matching.
- **Desktop: a per-app rule that matches running programs is no longer shown as "Not found
  on this computer".** The rule check looked for the exact path on disk while routing
  compared Store apps by package, so a Store app saved by the path of one version, as "Add to
  list" in Recent connections saved ChatGPT, showed the warning after its next update, beside
  "13 programs running". The check now looks for the app as
  routing compares it, reports nothing for a folder it may not list, and no longer warns that
  a rule "names one version". When a program is gone and the same one runs from a sibling
  folder that routing does not follow by itself, the rule is offered that path.
- **Windows: "Add to list" in Recent connections gives a Microsoft Store app the same package
  rule as the app list**, not the path of its current version.

### Security

- **`url` 2.5.9.** 2.5.8 overflowed the stack in `Url::origin()` on nested `blob:` URLs
  (RUSTSEC-2026-0592). Elarni's own code does not call it; the update clears the advisory check.

## 0.6.3 - 2026-11-22

### Added

- **Android and iPhone: a subscription's actions on touch and hold.** Touching and holding a
  subscription on Subscriptions, or its header on Home, opens Refresh, Pin or Unpin, Rename,
  Share and Delete subscription; a local group has no Refresh. A tap acts as before.
- **iPhone: a landscape layout.** With the iPhone on its side, the tabs move to a rail at the
  leading edge and Home shows the connection beside the server list, as Android does.
- **Desktop: search in Settings.** A "Search settings" field sits above the category menu in
  a narrow window and at the top of the category list in a wide one. Typing shows the matching
  settings from every category, grouped by category, as the working controls themselves: a
  setting is found by its label, its help text, the text under it, the heading it stands under
  or its category's name. Clearing the field or choosing a category ends the search.
- **Windows, Linux: the kill switch works in System proxy mode.** It was available only with
  System-wide VPN. With it on in System proxy mode, the computer's proxy setting stays on
  Elarni when the connection stops by itself, fails to start or restarts, so the apps that
  follow the system proxy get no connection instead of a direct one. Reconnecting, disconnecting,
  "Lift block" on Home, turning the kill switch off or quitting gives your own proxy setting
  back. Apps that ignore the system proxy are not covered in this mode, as they were not
  while connected; "Local proxy only" has no kill switch. A port that another program holds is
  never made the system proxy.

### Changed

- **Android: the subscriptions' update time sits at the top of Home.** The line that says when
  the subscriptions were updated is now the first thing on Home, above the connection, where a
  pull shows its spinner. A tap on it still updates them.
- **Home shows every server.** On every platform Home lists the servers by subscription and,
  under it, by country (Android and iPhone had the countries alone), and every subscription
  and country starts open. A group you close stays closed the next time the app starts, until
  you open it. On Windows, macOS and Linux a start therefore tests the servers of every group
  left open, not only the selected country's; on Android and iPhone **Test visible servers**
  tests the servers of the open groups.
- **By country in place of Recent.** The third list on Home is By country: the servers of all
  subscriptions under one header for each country, each with its subscription's name. All
  stays the list a start opens. The Recent list is gone on every platform; **Use last
  successful server** on Android and iPhone and the tooltip of a server's test button on
  desktop still lead to the last connection.
- **Favorites as one list.** The Favorites filter on Home shows the favorite servers of every
  subscription in one list, without subscription and country groups to open. Each server names
  its subscription and shows its country's flag: after its name on desktop and Android, before
  it on iPhone, where every server row has one. On desktop, showing the list tests the servers
  not tested lately.
- **Server rows under a subscription** show the transport and security after the protocol, not
  the subscription's name again. All platforms.
- **Android and iPhone: a compact Home.** Home drops the server search field and the server
  options menu, and its server rows are shorter, so more servers fit on a screen. A
  subscription's server list keeps the search (on Android also the country filter, the sort and
  favorites first); the sort and favorites first are in Settings › General as before.
- Desktop: adding a subscription stays on Home, even when it brings a routing profile; until
  now such an import opened the Routing page.
- **The "Current rules / active tunnel" test type is gone** from Settings › Server tests.
  It tested only the selected server, so with it chosen **Test all** showed a result for one
  server and nothing for the rest, and for that server it measured what **Proxy** measures
  (on Android it only worked while connected). A saved choice of it becomes **Proxy**. The
  connection health check and connection recovery still test the running tunnel. All platforms.
- **Desktop: a narrow window has its pages at the bottom.** Below 700 px the Home,
  Subscriptions, Routing and Settings icons are a bar along the bottom edge, where the phone
  apps have their tabs, instead of a row under the title bar. The back arrow in the title bar
  is gone: every page is one click away. A wider window keeps the sidebar.
- **Desktop: the Subscriptions page is a server table.** The page shows one subscription at
  a time, chosen in a menu that lists each with its server count and state, with Refresh and
  the "…" menu beside it. Under it are the server search, a Favorites filter, the order (the
  one Home uses) and "Test all", then the servers as a table: flag, name, protocol, latency,
  a star and a "…" menu per row. The cards that had to be opened one by one are gone.
  - A row now selects its server, as on Home; the selected one has a mark beside its name.
  - "Check exit" and "Server config" moved from buttons on the row into its "…" menu, which
    also has Rename, Share, Server IP type and Hide or Delete.
  - "Select servers" moved into the subscription's "…" menu, which also has "Details" and
    "Routing profile": both are under the table, and the menu scrolls to them.
  - The page opens on the subscription of the selected server. Home's server list is unchanged.
- **"Block traffic without VPN" is now "Kill switch"** on Windows, Linux, macOS, Android and
  iPhone, in English and Russian. It does what it did; the text under it and the messages that
  name it say "kill switch" too. Android's System info lists it under the same name.
- **Closing the window no longer quits Elarni.** On Windows, and on Linux desktops that show
  tray icons, the window hides to the tray, and the first time a notification says so; **Show
  Elarni** in the tray menu (on Windows also a click on the icon) brings it back. On macOS the
  app keeps running in the Dock and the menu bar, and a click on its Dock icon shows the window.
  The VPN, **Connect on untrusted Wi-Fi**, reconnecting, subscription refresh and expiry
  reminders go on with the window closed. **Quit Elarni** in the tray menu (Ctrl+Q on Linux, Cmd+Q on macOS)
  quits; on a Linux desktop without a tray closing the window still quits. Quitting on Windows
  and Linux now waits for the disconnect, for up to 8 seconds, before the app ends. Windows,
  Linux, macOS.
- **Trusted Wi-Fi networks are picked, not typed, on Android.** The list has a page of its own:
  it shows the Wi-Fi network the phone is on with a **Trust** button, each trusted network with a
  button that removes it, and **Add by name** for a network that is out of range. Each change is
  saved at once. Android shows an app neither the phone's saved networks nor, without location
  access, the name of the one it is on.
- **Trusted Wi-Fi networks are picked, not typed, on iPhone and iPad.** The list has a page of
  its own: it shows the Wi-Fi network the device is on with a **Trust** button, each trusted
  network with a button that removes it, and **Add by name** for a network that is out of range.
  Each change is saved at once. iOS tells an app the name of the joined network once the app has
  a VPN configuration, so the joined network appears after the first connection; the app now
  carries the Access Wi-Fi Information capability for it.

### Fixed

- **Android: one failing provider no longer keeps Home at "Updated 6 days ago".** The line
  showed the oldest subscription, so after an update in which one provider could not be reached
  it still gave that provider's old time. It now gives the time of the subscriptions that did
  update and counts the rest next to it ("Updated just now · 1 not updated").
- **iPhone: no lost-connection notice after you switch the VPN off in iOS.** Switching Elarni
  off in Settings or Control Center is your own stop, as Stop in the app is, so Home no longer
  reports "Elarni disconnected without a Stop from you" after it. A connection that iOS or a
  failure ends is still reported.
- **Desktop: Routing › Per-app routing in a narrow window.** A selected app's name was squeezed
  to one letter per line by its target menu, "Details" and the remove button. Below 620 px the
  name keeps one line (cut with an ellipsis, whole in its tooltip) with "Details" and remove
  beside it and the target menu across the width under it; the "Selected apps" heading has its
  own line above "Add apps" and "Recent connections". The Subscriptions and Routing pages use
  16 px side margins below 700 px instead of 24, and the per-app editor loses its extra 20.
- **System proxy mode** keeps the computer's proxy setting on Elarni while a running
  connection restarts for a server switch, a settings change or failover, with or without the
  kill switch. Before, the setting went back to your own for those seconds, so browsers went
  direct while the window said Reconnecting. A first connect still sets it once Elarni is
  listening, and a connection that stopped by itself gives your setting back unless the kill
  switch holds it. A failed attempt to set the proxy is tried again every 30 seconds instead of
  only at the next connect. Elarni also reads the setting back every 30 seconds: when another
  program (a second VPN client, a company agent, or you in the system settings) changed it,
  Elarni sets it again and says so; after the third change in one connection it leaves the
  setting to that program and says that apps may not use the VPN. Disconnecting gives back the
  setting you had before connecting, as before, or leaves the one another program was left.
  Windows, Linux.
- **Connect on untrusted Wi-Fi** now connects when the Wi-Fi name cannot be read, as it does on a
  network that is not on the trusted list. Before, an unreadable name turned the rule off without
  a word: on Android without location allowed all the time, on Windows 11 24H2 and later without
  Location access, and on Linux when neither NetworkManager's `nmcli` nor `iw` gave the name.
  Linux now also asks `iw` when NetworkManager does not manage the Wi-Fi, and finds it in
  `/usr/sbin` when PATH lacks it (as on Debian). Settings says what to allow. Such a network still
  connects once per join; on Windows and Linux the computer's addresses tell it from the next
  one, so a new address counts as a new join. With no trusted networks Android no longer reads
  the name at all. Android, Windows and Linux.
- **Connect on untrusted Wi-Fi was late on Android without mobile data.** Android told the app
  that Wi-Fi had changed only by mobile data taking over, so with mobile data off (roaming, a
  tablet) or on a move from one Wi-Fi network straight to another, a new network was noticed
  only by a check every 15 to 26 minutes, later with the screen off. The app now hears of every
  Wi-Fi change at once: while the rule is on and the VPN is off, a small watcher process of
  its own keeps running, with a **Watching for untrusted Wi-Fi** notification that has its own
  channel and can be hidden. It starts the app only when Wi-Fi changes. The earlier reports
  remain for when Android stops the watcher all the same.
- Subscription expiry reminders could be on and never show. Turning them on now asks for the
  notification permission, Settings › General says when Elarni's notifications are off and opens
  the permission request or Android's notification settings, and a reminder that could not be
  shown is no longer counted as shown, so it comes once notifications are allowed. Android.
- When the core stopped by itself while connected, Android closed the VPN, and nothing brought
  the connection back unless **Reconnect automatically** or failover was on. Without Android's
  own **Block connections without VPN**, every app's traffic then went on outside the VPN
  without a word. Now the VPN stays up and the core is started again inside it at once, twice
  in ten minutes at most; when that fails as well, the VPN closes and the connection shows as
  failed, as before. And a connection whose server passed traffic and then fails two checks in
  a row (**Connected · no traffic**) gets a new core, as on Windows and Linux; a server that
  never passed the check is still only reported. Android.
- **Select unreachable** in a subscription's server list ticks only servers whose latest test is
  still current and failed to reach the server. It also ticked servers whose test never reached
  them (another test was running, it was cancelled, the active-connection test ran without a
  connection, the server does not support the test type), was cut short by the 7-second limit
  of a whole-list test under a longer timeout, or was hours old. Windows, Linux, macOS, Android
  and iPhone.
- A subscription no longer stops updating for good after a network answered unexpectedly once
  (retried every two hours; Windows, Linux, macOS, iPhone) or after a device-limit reply
  (retried at its regular interval, and right after Reset device ID; all platforms).
- **Headless control:** the log can be read with `elarni-cli call readLogs` (pass
  `{"after": <seq>}` for only newer lines), and a daemon writes the notices the window would
  show, such as a connection that stopped or a scheduled refresh that failed, and the
  connection state they leave, to its output, so the Linux boot service has them in its journal
  (`journalctl -u elarni`). Before, the service logged one line at start. `elarni-cli shutdown`
  now stops the Linux boot service until `systemctl start elarni` or the next boot: before,
  systemd started it again five seconds later and it reconnected. A crash or a keyring failure
  still restarts it. The README said the `.deb` updates itself; that is so only where the app's
  window runs. A headless install never checks for updates: update a server with its package
  (`sudo apt install ./elarni_*_amd64.deb`). The log over `elarni-cli` works on Linux, Windows
  and macOS; the notices on Linux and Windows.
- A subscription refresh that removes the server an app is routed through no longer drops the
  connection. Before, the refresh stopped the tunnel, could not start it without that server,
  started the old one again and failed, so every scheduled retry cut every app's connections
  for a moment and the subscription never updated. The refresh now applies, the running
  connection keeps its old settings and a notice says to choose another server for that app.
  A change that would restart the tunnel without that server fails before the connection
  stops, and the next Connect names the app's rule. Windows and Linux; Android already did this.
- Leaving VPN mode (for System proxy or Local proxy only, or turning Xray TUN off on macOS) and
  coming back turns per-app routing on again with the mode it had. Before, leaving VPN mode
  turned it off for good, so after the return every app took the default route until per-app
  routing was turned on by hand. The app list and its targets were kept, and still are.
  Windows, Linux and macOS.
- With **Only listed apps** and none of the listed apps installed, Connect now says so instead
  of connecting. Before, the VPN then took every app, all of them went direct, and Elarni showed
  Connected. A settings change that would restart a running tunnel that way fails the same way,
  before the tunnel stops. Android.
- When a subscription update removes the selected server, macOS now moves to another server of
  that subscription (recovery's first allowed server when recovery is on) instead of
  disconnecting, and Windows, Linux, macOS and Android say that the server changed (iPhone
  already did). On Android only an update you start says so; a scheduled one changes the
  server without a word, as before.
- A subscription update that removes the server an app is routed through in Per-app routing
  now applies while connected: the connection keeps its settings, and a notice says to choose
  another server for that app. Until now every update of that subscription, manual or
  scheduled, failed until the per-app rule was changed. macOS; Windows, Linux and Android
  already applied it.
- Android: DNS is one choice, as on the other platforms. Settings › DNS no longer has a separate
  **Use profile DNS** switch, which could be off while **DNS override** named the provider
  profile or the routing profile; the connection then used 1.1.1.1 whatever the override said.
  A saved pair is read once: the switch off with **Provider profile** becomes **1.1.1.1**, which
  is what the connection used; the switch off with **Routing profile** keeps the routing
  profile's DNS, which the connection now uses.
- Windows, Linux, macOS, iPhone: a routing profile's managed domain list that was never
  downloaded is downloaded when connecting, for up to 10 seconds, so the first connection already
  follows its rules. Before, a list that had not been downloaded yet, for example right after an
  import, left its rules out until a later refresh. A download that fails does not stop the
  connection: the list is downloaded again over the connection, and on Windows and Linux Elarni
  says that its rules do not apply yet.
- A VPN core that stops by itself now shows as **Connection failed** ("Connection stopped
  unexpectedly") instead of **Reconnecting…** without end, and the app starts it again, twice
  in ten minutes at most. The tunnel keeps its routes, so nothing leaks until the core runs
  again. Connect restarts a failed core too, and Disconnect stops it. With Recovery on, its own
  retries restart the core; with **Xray TUN** on, only Connect does. macOS.
- Recovery that gave up no longer waits for Connect: it starts over by itself after 2.5, then 5,
  then 10 minutes, the waits Android and the headless Windows and Linux client use, and when
  the network comes back after it was gone. A connection that passes its check starts the waits
  over. On iPhone this runs while the app is open. macOS and iPhone.
- **A backup is no longer refused because of one subscription.** A subscription whose provider
  forbids backups, or one never updated (so whether its provider allows them is unknown), used
  to stop the whole backup. It is now left out, together with its provider's routing and the
  assignments and selection that point at it, and the message after saving names it and why.
  Nothing of such a subscription is saved, as before; the backup still fails when nothing else
  is left to save. Windows, Linux, macOS, Android and iPhone; `exportCatalogBackup` in headless
  mode returns the left-out subscriptions in `data.excluded`.
- **Android:** when Elarni's saved data could no longer be decrypted because its key was gone (as
  when a backup tool brings back its files without the key), it said to restart the app and
  offered no way out but clearing its storage in Android Settings. It now offers **Reset
  Elarni** and says to restore a backup or add the subscriptions again afterwards. A Keystore
  that only fails to answer still keeps the data and offers no reset.
- When Elarni's saved data can no longer be read (a damaged file, a Windows profile whose keys
  changed, a reset Linux keyring), Settings › Backup and repair offers **Reset**, which sets the
  unreadable file aside and starts empty so a backup can be restored; another running copy and a
  locked keyring now say what they are instead of advising a restore that could not work.
  Windows, Linux.
- With the **Kill switch** on, **Disconnect** and quitting Elarni now lift the block
  that a VPN which stopped by itself left, as Settings says. Before, Elarni showed Disconnected
  while nothing could leave the device until the next Connect, **Repair VPN setup** or a
  restart, and quitting while connected kept the block with no Elarni running. When the VPN
  helper itself crashed, Disconnect clears its block too, asking for consent where the system
  needs it; declined, it points at Repair VPN setup. Quitting for an update still keeps the
  block until the new version is back. Windows, Linux.

## 0.6.2 - 2026-11-18

### Added

- **ShadowQUIC servers.** `sq://` and `shadowquic://` links, subscriptions that carry them and
  Clash `type: shadowquic` proxies are imported, and a ShadowQUIC server is shared as an
  `sq://` link. A server has a user name and password, the server name its TLS hello shows,
  ALPN, UDP over streams or datagrams, 0-RTT, the congestion control (BBR, CUBIC or New Reno)
  and the packet size; without a packet size the client keeps its own (1300 bytes at first,
  1290 at least). The client is ShadowQUIC v0.4.12 with fixes that change nothing on the wire
  (`Vendor/shadowquic/ELARNI_PATCHES.md`), so it works with a stock server. A ShadowQUIC server
  connects directly, not through another server, and has no TLS fingerprint, multiplexing or
  fragmentation settings. All platforms: on Windows
  and Linux the client is a program next to the core (`elarni-shadowquic`), which the
  elevated helper starts only when it is the build its core was made with; on Android,
  iPhone and in the macOS tunnel it runs inside the core.
- **TLS fingerprint** in Settings › Advanced chooses which browser a connection to a server
  looks like: Chrome, Firefox, Safari, a random browser, or a randomized hello. The default,
  **From the server**, keeps what each server's link names (Chrome when it names none), as
  before. Until now the fingerprint came only from the link, so when a network began to block
  one browser's hello, a server from a provider could not be switched to another. The choice
  applies to TLS and Reality servers over TCP, to a provider profile's backup servers and to a
  per-app server; Hysteria2, TUIC, ShadowQUIC and WireGuard servers have no such fingerprint. All platforms.
- **Live speed.** Home shows the download and upload speed under the Connect button while
  connected, whatever the Monitoring setting. The counters are read only while the app is on
  screen. All platforms; on iPhone the speed is that of the tunnel interface, packet headers
  and directly routed traffic included.
- **Windows, Linux and macOS: what the phone apps already had.** The server list has sort and
  location menus next to its title and **Use last successful server**. A server's menu shares
  that server as a link or QR code and sets its IP type (before, only the selected server's).
  The routing editor edits the GeoIP and GeoSite addresses, managed domain lists and static
  DNS hosts, which until now needed the JSON. A routing profile's menu shares it as text and
  a QR code and exports its JSON to a file, and its details refresh its managed domain lists
  on demand. The share dialog saves its text and its QR image to a file, and the selected
  servers of a group without a subscription link are shared as links. A running latency test
  can be cancelled. Subscriptions have **Refresh all**, the latency test settings are kept
  between launches, and Settings › System opens the licenses.
- **macOS: Shortcuts actions** Connect Elarni, Disconnect Elarni and Toggle Elarni, as on
  iPhone. They ask for no confirmation and tell Shortcuts when they fail, for example with no
  server selected.
- **Android, iPhone and macOS: a server's configuration** can be viewed and replaced, as on
  Windows and Linux; the edit must still be one Xray server, and a provider that hides its
  servers hides this too.
- **Android:** per-app routing gains presets for Discord, Telegram, Claude, Steam, browsers,
  and social and video apps next to Russian apps; the routing editor gains the **Russian sites
  direct** preset; Home offers **Use last successful server**.
- **iPhone: a widget** for the Home Screen and the Lock Screen shows whether Elarni is
  connected and to which server; the Home Screen one connects and disconnects. **Hide VPN
  icon** is now in Settings, under Advanced network routes, as on macOS and Windows.
- Elarni updates itself. An install from the Windows MSI, the Linux `.deb` or AppImage, or the
  Android APK downloads the release its update feed names, in the background, and installs it
  when that asks for nothing and interrupts nothing: on Windows at the app's next start (through
  the ElarniVPN service, so without a consent prompt), for the AppImage in place (used from its
  next start), on Android while the VPN is off once Elarni is allowed to install apps. Otherwise
  the Home notice has **Install** instead of the link, and a tunnel that was connected comes back
  after the update. A connected tunnel is never cut for an update nobody started. Turn it off with
  Settings › General › **Install updates automatically**; the notice then asks first. The portable
  ZIP and archive, macOS and iPhone keep the notice with the link. This starts with the release
  after this one: an older build has to be updated by hand once.
- Releases are signed with a release key (`SHA256SUMS.sig`). A build installs only a package
  whose checksum list carries that signature and whose version is newer than its own, whatever
  the update feed or the server behind it says; the installers check this again themselves
  before they run anything with administrator rights. See docs/RELEASING.md.

### Changed

- **iPhone:** a new install requires a generated login on its local SOCKS and HTTP listeners,
  as the other platforms do, so other apps on the phone cannot use them unasked. An install
  that never saved its settings counts as new. Set your own login, or turn it off, in
  Settings › Connection.
- For contributors who use coding agents: the checked-in Claude Code settings no longer refuse the
  VPN acceptance scripts and the route, DNS and adapter commands, and `AGENTS.md` no longer tells
  agents to leave them alone. The rules matched any command that named one of the scripts, so
  they also refused reading or searching them.
- For contributors: `scripts/flaky-report` ranks the tests that failed and then passed, from
  local check results and from the results CI uploads, and `scripts/ci-wait` waits for a CI run
  and prints only each job's result and the end of the failed jobs' logs.

- **Windows and Linux: testing many servers no longer copies the whole server list for each
  one.** Every server test through its own proxy made a full copy of the catalog first, so a
  **Test all** of a large subscription did work that grew with the square of its size and
  held up the window. A test now reads the one server it needs.

- **A release is published only when its draft is one build's whole set of files.** The publish
  stage checked the files that were in the draft, each on its own, so a draft with a package
  left out, or with an authentic package of another build, passed. It now requires every file
  the build attested, all from one run at the draft's commit, the tag of the version that was
  built, and a signed APK whose contents are the build's unsigned one.

- **`elarni-cli status` no longer builds the server list to read the selected server.** A
  monitor that asks for the status every few seconds repeated that work for every server.

### Fixed

- iPhone: when the VPN switch of iOS itself (Settings, or the VPN button in Control Center) stops
  Elarni while On Demand is on, iOS starts the VPN again at once, and nothing said why. iOS gives
  an app no way to prevent that restart, so Elarni now posts a notification after it with a
  **Turn Off** action that stops the VPN and On Demand together, and Settings › General says
  which controls keep the VPN off (the app, its own Control Center control, a shortcut).
- **Android, app in Russian:** the routing editor showed the domain and address lists of
  Direct, Proxy and Block as empty and lost what was typed into them, because it looked the
  lists up by the translated name. They show and save now.
- A change of per-app routing while connected no longer restarts the tunnel on Windows and
  Linux. Before, adding or removing an app, changing its target or switching the mode stopped
  the tunnel and started it again, which cut every connection of every app for about two
  seconds (a voice call dropped, for one). The running tunnel now takes the new rules as it
  is: connections that are open stay open on the route they have, and new ones follow the new
  rules. A change the running tunnel cannot take (another server, the routing profile, routes,
  DNS, a listener, or another server for an app that already had one) is restarted as before.
- Automatic failover now leaves a server that refuses or closes connections even when that
  server never passed a health check since the last change. Before, a server that was already
  down at Connect, or that went down right after failover was turned on, stayed selected and
  shown as connected, and the backup was never used. A server that never passed a check is
  still not restarted in place, and a fixed server is not touched. With no other allowed server
  to move to (a list of one, or the others just tried) such a server is kept as it is, not
  retried until the retry limit ends the connection. Windows, Linux, macOS and iPhone; Android
  already did this.
- The **Review download destination** prompt lists the addresses of the download. Android
  showed `null://null` and iPhone showed "Invalid destination", because the core sent
  fingerprints where the apps expected addresses; the Windows, Linux and Mac prompt named only
  the risk, and `elarni-cli` returned no `destinations`. When a connect address overrides the
  URL's host, the prompt names it beside the URL.
- A subscription on this device's own network refreshes while Elarni is connected: a URL with a
  loopback, private, link-local or unique-local address, or a `.local` or dot-less name. The
  name is looked up by the system, a `.local` name also by mDNS when the system cannot answer
  (FakeDNS in VPN mode, Linux), and the address is tried from this device first, then through
  the tunnel. Before, both went through the tunnel and the refresh failed. A name that a
  resolver in the tunnel answered with a private address stays in the tunnel, as it was: that
  is the server's network. Android, Windows and Linux.
- An address with an IPv6 zone (`fe80::1%eth0`) is classed as a local address in the download
  review, like the same address without the zone.
- The `elarni-cli` daemon names why it could not start: `storage_corrupt` for a damaged catalog,
  `keyring_unavailable` for a keyring it cannot open, `socket_unavailable` for a socket it
  cannot bind, instead of `operation_failed` or `daemon_unavailable` for each. Text that is
  not a code is still never printed.
- A VLESS or Trojan link without TLS, REALITY or VLESS encryption that points at a public
  address is refused with that reason. Before, the message said the profile was unsupported
  and suggested another format, also for a new subscription, on the refresh failure card and
  for a Clash file. The rule itself (the core accepts such a server only on a private network)
  is unchanged. All platforms.
- Android asks to run without battery optimization. Doze cut the VPN while the phone slept,
  because the app was not exempt and never asked. With System VPN on, Home shows the request
  once and **Settings > General** has a **Run in the background** tile; both open Android's
  own dialog.
- Refresh times written while the device clock was far ahead come due once the clock is set
  right, on Android, macOS and iPhone as on desktop. Before, the subscription, managed lists
  and geofiles were not refreshed again until that date, and the Mac's maintenance timer slept
  until it.
- A pasted or file-imported list with a server the compiler refuses imports the rest and counts
  the one left out, as a subscription refresh does. Before, the whole import failed. Windows,
  Linux and Android; on macOS and iPhone a list that joins the local group keeps its count of
  skipped lines.
- A Clash file with a rule that points at a proxy the import skipped, or with `no-resolve` on
  an `IP-CIDR`, `IP-CIDR6` or `GEOIP` rule, imports. The rule to the skipped proxy blocks what it
  matched, so a later rule such as `MATCH,DIRECT` does not send that traffic another way, and a
  file whose `MATCH` rule points at a skipped proxy is refused with that reason; when
  every IP rule has `no-resolve` the profile does not resolve names for IP rules, otherwise it
  keeps resolving them.
- A share link is not written for a server whose link would lose settings (mux, a target
  strategy, pinned certificates, extra WebSocket headers, socket options, a VLESS reverse
  proxy): that link imported as another server. **Share links** says so and the server is
  shared from its own page, as JSON. A WebSocket `Host` header is written as the link's host.
- Android: a subscription refresh while connected no longer fails for good when the feed
  dropped both the selected server and a server an app is pinned to. The running tunnel stays
  as it is and the next Connect names the app's rule.
- Android: when the saved catalog cannot be read, the failure offers **Reset Elarni** behind a
  confirmation. Before, the only way out was clearing the app's data in Android settings.
- Recent connections shows an IPv6 destination as `[2001:db8::1]:443`, not with doubled
  brackets. Android, Windows and Linux.
- Windows and Linux: notices and stored failures are in the interface language: links that
  failed to import, a connection that stopped, the system proxy, a scheduled refresh, the
  system settings and occupied-port errors, a routing profile's geofile and managed-list
  failures, a per-app paste that is not JSON. Before, they were English in a Russian interface.
- Tests: changes to four tests that failed in CI and passed on a rerun, and more output from a
  fifth. The heap check of waiting UDP reads counted buffers that were on their way back
  to the pool (Linux, race build). The split-HTTP keep-alive close test's relay reset the
  connection where a network path would only close its half, which on Windows discarded the
  server's last response. The Android untrusted Wi-Fi test waited 10 seconds for Wi-Fi to turn
  on and did not wait for the leave watch before its last join. When an Android catalog test
  failed and was rerun alone, the rerun wiped the catalog the restart tests read, so they
  failed too; the test that leaves it now always runs apart, right before them. The Android
  TUIC test's empty first reply is not explained yet: the failure now says which connection it
  was, and the run prints the TUIC peer's log.

- **Apps sent to the same server or local proxy share one connection handler.** Per-app routing
  made a separate outbound and rule for every app, even when several apps had the same target,
  because it compared a new target with ones it had already tagged. Desktop and Android.
- **A provider's routing rule named like a per-app rule no longer stops the connection.** A
  provider profile may name its rules (`ruleTag`). One named `elarni-per-app-listed-0`, or any
  other name per-app routing gives its own rules, made the core refuse the whole configuration
  once per-app routing was on, and was shown as a per-app decision in recent connections. Such
  a rule is renamed when the profile is compiled. All platforms.
- **A Clash profile in global mode follows the server chosen in it.** Such a profile (Remnawave's
  default Mihomo template is one) was imported without its rules and with nothing in their
  place, so the first server of the file carried everything, whatever was chosen in its
  groups. It now has the **GLOBAL** group Clash gives global mode: the file's own, or one that
  offers every group, every server and DIRECT and starts on the file's first group. A
  subscription gets it at its next refresh; a file or pasted profile has to be imported again.
  All platforms.
- **A reset device ID stays reset on macOS and iOS.** The refresh right after a reset sent the
  new ID and then dropped it, so every later refresh sent the old ID again; choosing a server in
  a profile's group dropped it too. The new ID now stays with the subscription.
- **A subscription that could not be deleted keeps its connection on macOS and iOS.** Deleting
  the subscription of the connected server stopped the tunnel, removed its saved plan and
  turned reconnecting off before the deletion was written; when the write failed, the
  subscription stayed and the connection was gone. The tunnel is now stopped once the deletion
  is on disk, and comes back if the deletion fails after that.
- **Desktop: testing with "Current rules / active tunnel" no longer marks other servers
  unavailable.** That test measures the selected server's tunnel, but **Test all** asked for
  every server, got the selected one's result each time, and showed the rest as failed. It now
  tests the selected server once and leaves the other rows alone; a row's own test button
  tests the selected server too.
- **Desktop: a subscription's page shows and copies the device ID its next download sends.**
  The ID depends on the address the subscription is downloaded from, but the page kept the
  one it looked up first: after the provider moved the subscription, or after a switch to a
  download format served from another address, **Copy** gave the old ID. A lookup that failed
  stayed "Unavailable" until the app was restarted; it is now tried again.

- **iPhone: a pasted or file import says which servers it left out.** The notice about servers
  Elarni does not support, and the ones about routing rules or DNS it could not apply, were
  shown only for subscriptions with a link. A local import now shows them on its page too.

- **"Resolve if no rule matches" no longer lets an IP rule take a name that a later domain rule
  matches.** The compiled rules end in a catch-all, which would have ended the first pass for
  every connection, so this setting was compiled as "resolve when needed": a name was resolved
  at the first IP rule, and a direct rule for an address range won over a proxy rule for the
  name itself further down. The setting now does what it says, in a routing profile and in a
  provider's own profile: the rules are read by name first, then with the name's addresses,
  and only then does the catch-all apply. All platforms.

### Security

- Go 1.27.4 (GO-2026-6859 in `net/http`), hickory-proto 0.26.3 (RUSTSEC-2026-0487 to
  RUSTSEC-2026-0491) and rustix 1.1.6 (RUSTSEC-2026-0486).

## 0.6.1 - 2026-11-14

### Changed

- Settings no longer wait for **Save changes** to take effect. A switch saves itself about a
  second after you flip it (several flipped in a row make one save), and the page title shows
  **Saving…** then **Saved** on Android and iPhone. What you type or pick is saved when you
  leave the page (Back, another tab, or a page opened from it) as well as by **Save changes**;
  **Cancel** drops it, and iPhone settings pages gain a **Cancel** for that. A value the app
  refuses keeps its page open with the reason instead of being dropped. On desktop this
  applies to the per-app routing page and its route checkboxes; other desktop settings already
  saved at once. Routing-profile editors on iPhone and desktop keep their Cancel and Save.
- Removed the core's batch latency test and its start from a config file: no platform calls
  them.
- A subscription no longer fails as a whole because of one server Elarni cannot read. A link,
  Clash proxy or Xray JSON entry that is unsupported or malformed, or that Elarni reads but
  cannot run, is left out, the others are added, and the subscription's page shows how many
  were left out and of which kind (**Servers not added**). Before, one such line cost every
  server: a 12-server subscription with one unusual TUIC link added nothing. A reply with
  nothing Elarni can use still fails and keeps the servers from the last refresh. All
  platforms.
- A routing profile from a provider that Elarni cannot apply no longer fails the subscription
  it came with: the servers are added or refreshed without it, and the subscription's page
  says so. All platforms.
- **TLS fragmentation** applies to the servers that can carry it (VLESS over TCP or XHTTP with
  TLS or Reality); every other server now connects without it. Before, the setting could not
  be turned on while the list held a Trojan, Hysteria2, TUIC or similar server, a refresh that
  brought one failed while it was on, and such a server could not be connected. All platforms.
- A subscription address that starts with `http://` and names a domain is accepted after the
  same "not encrypted" approval as one with an IP address; it was refused as invalid. Panels
  such as 3x-ui serve subscriptions over plain HTTP unless a certificate is set.

### Fixed

- Links and files written by other clients and panels now import: TUIC links from v2rayN
  (`uuid%3Apassword`) and NekoBox (`/` before `?`), and TUIC links that write the
  skip-verification flag as `insecure` or `allowInsecure`; AmneziaVPN `vpn://` keys;
  `happ://add/<address>` subscription links, which Remnawave's **Add subscription** button
  writes; VMess links with `allowInsecure`; WireGuard files with repeated `Address`, `DNS` or
  `AllowedIPs` lines or a `ListenPort`; Hysteria2 links with `obfs=gecko` and packet sizes;
  the old all-base64 `ss://` form; and Remnawave's default Clash (Mihomo) file. All platforms.
- Happ routing profiles that leave the DNS fields or the name empty (Happ's own documented
  example and the default output of its routing builders do) were refused; missing DNS now
  takes Happ's defaults. Fields that other clients add are ignored, and the header forms
  `off`, `...?data=` and, again, a link without a scheme (`://routing/...`, which INCY
  documents for providers) are accepted. On Android a `routing` header that is not a link
  (bare base64 or JSON, `off`) was ignored. All platforms.
- An Xray JSON subscription with extra top-level sections (`meta` in Happ JSON, Xray's
  `version` and `geodata`) passed the importer and was then refused: a new subscription
  vanished and a refresh failed without a recorded reason. Such sections are ignored. On
  Windows and Linux a refresh whose servers cannot be used is now recorded on the subscription.
- A server whose address is an IPv6 literal imported but could not connect on Windows and
  Linux.
- A subscription Elarni could not read was reported as a failed download ("Refresh to retry",
  "The last working servers were kept" beside 0 servers). The message now says the content is
  not supported; Remnawave's device limit, which arrives as an empty successful reply, shows
  the device-limit message. A failed Connect names missing or incomplete routing data, an
  oversized routing profile and conflicting route settings instead of "retry the server", and
  an import says when the file is too large or the image has no QR code.
- **Excluded routes** and **Included routes** take entries separated by commas or spaces and a
  bare address (`192.168.1.10`); these were saved and then refused. A per-app rule pasted with
  Explorer's quotes (`"C:\…\app.exe"`) is saved without them. Windows, Linux and macOS.
- macOS and iPhone: after a move to a new device, where the server list arrives without the
  Keychain items, every add, refresh and routing change failed until the old subscriptions
  were deleted. Such servers are passed over until their subscription refreshes. Closing the
  file or QR image panel on macOS no longer shows "Could not import".
- Android: with per-app routing on and the VPN connected, a refresh that dropped a server an
  app was pinned to failed for as long as the VPN stayed up. A subscription address written
  as `Https://` is accepted. The message for a catalog that cannot be opened names the way out.
- System info and diagnostics on Windows 11 named the system "Windows 10".
- Windows: an MSI upgrade to a build whose bundled Visual C++ runtime is older than the
  installed build's removed the runtime files, and Elarni no longer started. The installer now
  copies every file whatever version is installed.
- Windows: uninstalling while Elarni was running left the app and its core running, with the
  network still captured for half a minute or more and their files locked. Uninstall now closes
  the app first.
- Windows 10: the first Connect after an install, an update, an app restart or a crash of the
  VPN helper often failed after about 20 seconds, and only the automatic retry connected. The
  network adapter of the previous helper stays behind as a device that is gone; the new adapter
  was created while Wintun was still removing it, which Windows 10 answers after 15 seconds
  with an error. The helper now waits for the removal, which takes a fraction of a second.
- Automatic failover never left a server whose service had stopped: only check timeouts counted
  against the selected server, so one that refused or reset connections stayed selected, shown
  as connected, with nothing passing. A server whose check passed before and whose requests are
  now refused, reset or closed counts as failed. Windows, Linux, macOS and iPhone.
- With **Block traffic without VPN** on (Windows, Linux), traffic no longer leaves outside the
  VPN while the app restarts the tunnel: on a server switch, a per-app or route change,
  failover, or the reconnect after the tunnel ended by itself. It left for 60-240 ms on each
  change and about a second after a lost tunnel. The block now stays in place between the two
  tunnels, and a reconnect under it uses the server address it last resolved, so a server
  named by a domain reconnects without a lookup the block would refuse. Disconnect lifts it.
  On Windows the reconnect after a crashed VPN helper also keeps the block the helper left,
  instead of clearing it first (up to 19 s outside the VPN on a slow PC).
- Windows, with **Block traffic without VPN** off: a connection an app opened before Elarni
  connected, or in the fraction of a second between two tunnels (a server switch, a changed
  setting), stayed outside the VPN for as long as it lived, as Windows keeps a connection on
  the network it was opened on. When the tunnel is up, Elarni now closes such IPv4 connections
  whose destination the tunnel takes, so the apps reconnect through it. Connections to excluded
  routes and local networks, and those of apps that skip the tunnel, are left alone.
- **Share** of an XHTTP server imported from a link or subscription wrote a link no client could
  import (its `extra` listed every unset option, with ranges in a form the importer rejects), and
  for a server from Xray JSON or Clash it left out the options a link has no parameter for
  (connection reuse, padding, a separate download server). The link now carries the options the
  server sets in `extra`, and nothing else. Android, iOS and macOS.
- A link whose `extra` names a separate XHTTP download server lost that server's own options
  (padding, connection reuse, headers) on import. All platforms.
- **Share** changed the server in a few more cases, and now keeps it: a Hysteria2 server lost its
  congestion settings, the upper bound of a port-hopping interval and the hop mode (what the
  link's own parameters cannot say now travels in `fm`); a Trojan server without TLS came back
  with TLS; a SOCKS user name with a colon was cut at it; a WebSocket server behind a front name
  without an SNI got its Host header as the SNI; and a server from Xray JSON with `sendThrough`
  was named after that address. Android, iOS and macOS.
- A Hysteria2 link's `hop-interval` may be a range of seconds (`30-60`), as Clash writes it; it
  was refused. All platforms.
- `elarni-cli call inboundCredentials`, which the headless guide names as the way to read the
  listener login in proxy-only mode, was refused as `unsupported_method`.
- System proxy mode: when setting the proxy failed and undoing it failed as well, disconnecting
  or quitting left the system proxy pointing at the closed listener until the next start. The
  disconnect now restores the saved setting. Windows and Linux.
- System proxy mode: starting a second copy of Elarni while the first was connected put the
  system proxy back to the user's setting, so the first copy showed "connected" with the proxy
  off. A copy that is refused now leaves the setting alone. A saved setting that cannot be read
  (a damaged file) no longer refuses every later connection: it is reported once and dropped.
  Windows and Linux.
- Subscriptions, managed lists and routing data stopped refreshing by themselves, for as long as
  years, after one refresh under a system clock that was set far ahead and then corrected.
  Windows and Linux.
- A damaged copy of a route's geofile in the routing data cache failed every connection and every
  **Refresh routing data** with "invalid geodata" until the provider published a new file: the
  server only answered that the file had not changed. The file is now downloaded again. A cache
  that lost a prepared file is prepared again at Connect. Windows and Linux.
- A catalog that could be decrypted but was not a catalog failed with an internal error
  instead of the "catalog is damaged" message. Windows and Linux.
- Per-app routing on Linux: a connection that a listed app had opened before Connect could be
  reset and shown twice in **Recent connections** when the system's connection table changed
  while it was being read.
- Logs and diagnostic reports kept host names written in a non-Latin script (`пример.рф`); they
  are now redacted like other host names. All platforms.
- After a network change (Wi-Fi to mobile data, a new default route), an XHTTP or TUIC connection
  on the dead path could stay open with its apps stalled until the idle timeout, 30 s or more:
  data the old network had still delivered counted as the server's answer to the check. The
  check now ignores what arrives in its first second. All platforms.
- Android: with the keyboard open, screens lost the height of the navigation bar a second time,
  and the per-app app search showed nothing it found: the results were below the visible list,
  after the selected apps. The results now come right under the search field.

## 0.6.0 - 2026-11-10

### Added

- The per-app rule list shows, on Windows, macOS and Linux, how many running programs each rule
  matches, and warns when a rule may match nothing: a missing file, a folder without its
  trailing separator, a script, link or launcher, the wrong letter case, or a rule for one
  version of an app. **Use suggested rule** fixes it where it can, including rules for one
  Squirrel or Claude Code version saved before. On Android, listed apps that are not installed
  are shown and can be removed.
- **Recent connections** in the per-app editor, on Windows, macOS, Linux and Android, lists the
  connections apps opened while connected: which program made each one, its address, and
  whether a per-app rule took it and where it went. **Add to list** adds a program that is not
  listed, such as an app's helper or updater. The list stays in memory until you disconnect.
- Per-app rules for Flatpak apps and AppImages on Linux, and for services on Windows. A Flatpak
  app matches by its app ID, with every program in its sandbox; an AppImage by its image file,
  although each start mounts it at a new path; a service by name, in the svchost process that
  runs it, so Delivery Optimization or Windows Update can take a target of its own. The app list
  offers them and **Recent connections** adds them. Before, a running AppImage was listed by its
  mount path, which the next start changes, and Flatpak apps could not be listed until running.
- **Include programs it starts** for a listed app on Windows, macOS and Linux: its rule also
  matches the programs the app starts, and theirs, such as helpers, updaters or a terminal's
  command-line tools. A program listed on its own keeps its own target, and **Recent
  connections** shows which listed app started a program.
- Listed apps that were running before Elarni connected no longer keep connections that skip
  per-app routing: on Windows (IPv4) and Linux, Elarni closes them when the tunnel starts, so
  the app reconnects through it. Where the system does not allow that (IPv6 on Windows,
  macOS), **Recent connections** lists them and asks you to restart the app.
- The Windows app list also offers the Start menu's apps and installed Microsoft Store apps,
  such as Claude, when they are not running. On Linux the list loads without freezing the
  window.
- **Presets** in the desktop per-app app list add Discord, Telegram, Claude with Claude Code,
  Steam with its games, or browsers at once, each with the rule the app list gives it.

### Changed

- The Xray core is updated to upstream v26.11.9. Its default TLS fingerprint ("chrome") now
  imitates Chrome 155, which upstream reports Russia's TSPU does not target yet. Multiplexed
  (mux) connections no longer write to their server connection from two streams at once and
  carry up to 128 streams each by default, an inbound that fails to start is closed instead of
  left half-registered, and on Windows the VPN adapter's IPv6 setup errors are only logged
  when the registry's DisabledComponents turns IPv6 off, with IPv6 outside the tunnel blocked
  when leak blocking is on.
- Per-app routing finds each new connection's app faster. Windows and Linux ask the system for
  that one connection instead of reading every socket on the computer (Windows: 0.64 ms to
  25 µs per connection), and macOS reads its socket table once for connections that open
  together, as a browser's do (64 at once: 70 ms to 2.6 ms).
- Editing a large catalog no longer compiles every server again. On Windows and Linux a favorite,
  rename, selection, settings toggle or refresh compiles only the servers it changes: with 300
  servers on a route of 20,000 rules, a favorite or a rename went from 9.4 s to 0.11 s. Android,
  macOS and iOS likewise validate and hash a server again only after its configuration, its
  routes or the settings changed.
- A TCP or ICMP ping on Windows, Linux and Android no longer compiles the server's whole
  configuration to learn its address; the address is kept until the server changes.
- macOS and iOS read, compare and reduce geofiles off the main thread, so the window stays
  responsive during a geofile refresh. Android no longer copies the stored geofiles for each
  rule edit that keeps them.
- The Subscriptions page builds the server rows of open cards only, the Settings page builds
  only the open category, and the Windows Settings page asks for the VPN service state at
  most once in 30 seconds instead of starting a core process on every reload.
- Android scans QR codes from the camera's brightness plane instead of encoding each frame
  as JPEG and decoding it again.
- The macOS and iOS tunnel resolves a profile's server hosts together when it starts.
- Removed code no platform runs: the tun2proxy command-line programs, its HTTP and SOCKS4
  proxies, virtual DNS and UDP gateway, with 90 crates from the Rust lockfile, and libXray
  methods no app calls. A routing link that starts with `://routing/`, without a scheme, is no
  longer imported on Windows, Linux, macOS and iOS; nothing writes such links and Android
  never took them.
- The per-app app list opens faster the second time on macOS and Linux: app icons are drawn
  once and kept until the app or the icon theme changes, and macOS builds the list off the
  main thread, so the window no longer freezes while it loads.

### Fixed

- On iPhone, iPad and Mac, apps' UDP traffic now reaches the server. The packet bridge's relay
  socket to the core was not bound to loopback, and iOS and macOS dropped every datagram sent
  on it, so each UDP session ended at its first datagram: games such as Brawl Stars fell back
  to TCP and lagged in battles, and QUIC fell back to TCP.
- Traffic to the VPN adapter's own subnet, other than DNS, now ends at once instead of
  following routing rules for private ranges. On Android, each connect set off 2,000-4,000
  Private DNS connections (port 853 on the VPN's resolver) in about 3 s, and the check hung
  for 16 s or more before it fell back to plain DNS; Windows sent subnet broadcasts (NetBIOS,
  Spotify device discovery) out the direct route.
- A Windows connect that the PC slept through no longer fails when it wakes. The helper's
  30 s startup wait and the app's 120 s connect wait counted the time asleep, so both ran out
  at resume, the helper exited and the app started over, although the core was ready seconds
  later. Helper and app waits on Windows now count only the time the PC is awake.
- A server test whose XHTTP connection was still connecting when the test ended no longer
  holds its cleanup for up to 16 s. The app reported "An earlier server test is still
  closing" meanwhile (iOS refused further tests until restarted), and macOS paused automatic
  server switching. The XHTTP dial now ends with the test, and with the connection it served.
- Android now names the app of short-lived connections, so per-app rules match them. About
  16% of QUIC flows to Vision servers and about 5% of TCP connections had no app, because the
  app had closed its socket by the time routing asked Android who owned it. With per-app rules
  on, the VPN now asks when the connection arrives and remembers a socket's app for 30 s after
  it closes.
- Automatic server tests on Windows, macOS and Linux start 50 ms apart instead of 40 ms, so
  uneven dial times no longer put three handshakes to one server name within 50 ms. Russia's
  TSPU freezes a server name after more than three handshakes that close together.
- Per-app rules for the Claude app on Windows, and for other Microsoft Store apps, stopped
  matching after each app update: the app runs from a folder named after its version. Rules now
  match the package in any version, including rules saved before. Claude Code, which the Claude
  app keeps in a new `claude-code\<version>\` folder with each update, is listed as one app that
  matches every version, on Windows, macOS and Linux; so are Snap apps on Linux, whose path
  changes with each refresh. Pick Claude Code and Snap apps again from the list to get the new
  rule; Store app rules need nothing.
- On Android, an app listed for per-app routing that was installed or reinstalled while
  connected missed its route until the next connect: in **Only listed** it stayed out of the
  VPN, and in **Bypass** its traffic went through it. Elarni now builds the tunnel again when a
  listed app is installed, removed or reinstalled; app updates change nothing.
- In per-app **Only listed** mode on Windows, macOS and Linux, a connection whose app could not
  be found went direct, so a listed app's connection could leave outside the VPN. It now follows
  normal routing, as on Android. Such traffic can include WSL, virtual machines and containers:
  to send it direct as before, add **Unidentified traffic** to the app list and set it to Direct.
- On Linux, per-app rules missed IPv4 connections made from IPv6 sockets, which Java apps
  (Minecraft, JetBrains IDEs) and many servers use, and rules stopped matching a program whose
  package was upgraded while it ran, until it restarted.
- macOS and iOS: an app whose connection the packet stack had given up, as during a server
  switch or at the session limit, waited for its own timeout. The stack now resets such a
  connection, so the app reconnects at once.
- macOS: an automatic switch that the provider refused (missing credentials, an invalid
  configuration, failed resolution) disconnected the VPN although the previous server was
  still running. The VPN now stays up, and the app waits 20 s for a switch reply instead of 15.
- macOS and iOS: after one failed geofile refresh, such as being offline at the weekly
  refresh, a routing profile's geofiles were never refreshed automatically again. A failed
  refresh is retried after 15 minutes.
- **Share** wrote a dead link (`<nil>:0`) for a server imported from Xray JSON in the usual
  `vnext` or `servers` form, and dropped a REALITY `publicKey` and the path, host, mode and
  seed of transports named `websocket`, `splithttp`, `mkcp` or `tcp` with an HTTP header.
- Android and iOS: saving a second new routing profile overwrote the first one's rules,
  because both started as "My routing". A new profile now takes a free name.
- Android: with **Disconnect on sleep**, a tunnel started while the screen was off (tile,
  boot, an external request) was restarted when the screen turned on.
- A subscription's connect address and HTTP Host override were also used for its mirrors and
  the provider's fallback URL, which are other hosts, so none could take over.
- A UDP flow in VPN mode could start again from a new source port after its full queue
  refused a datagram.
- Windows and Linux: a system proxy restore that failed was retried and reported every
  second, and the proxy was never set.
- macOS: the log level dropdown stayed disabled after a change.

## 0.5.1 - 2026-11-08

### Changed

- Reduce routing-data size is on for new routing profiles and for imported ones that do not
  set `UseChunkFiles`, as in Happ's routing builder: the core loads only the geofile records the
  rules use. With the full Russian geofiles this cut the Windows helper's peak memory from 478 to
  112 MB, and on macOS and iOS it lets geofiles over 64 MB load at all.
  A profile that sets `"UseChunkFiles": "false"` keeps full files.

### Fixed

- The VPN no longer relays LAN discovery (mDNS, SSDP, LLMNR, WS-Discovery) or other multicast
  and broadcast packets that Windows sends out of the VPN adapter. Windows sends a burst of them
  at each network change, enough to fill the 1024-slot UDP session table with new sessions; the
  VPN then refused every new UDP flow, DNS lookups included, for about 10 s. On a recorded
  session this happened about half the time, and pages waited up to 14 s for DNS.
  The same applies on every platform: Linux and Android share that packet stack, and the
  macOS and iOS tunnels now drop such packets as well instead of opening a UDP session for
  each, which once their table was full refused every new one.
- VLESS with XTLS Vision refuses QUIC (UDP/443) before connecting to the server, rather than
  after a full connection and TLS/REALITY handshake. Browsers retry QUIC often: one recorded
  10-minute session made 2,611 such handshakes for nothing.
- LAN discovery (mDNS, SSDP, LLMNR, WS-Discovery, DHCP-style broadcast) stays on the local
  network instead of entering the VPN, where it reached no device: apps that send it without
  choosing an interface now send it on the local network. The tunnel's routes leave out
  multicast and the limited broadcast on Windows, Linux, Android, macOS and iOS; on Windows
  the VPN adapter's own discovery routes also lose to every other interface. Block traffic
  without VPN (and Android's lockdown) keeps them in the tunnel; on Windows the adapter now
  gets its own routes to them, which Windows did not always create, so mDNS no longer left
  through the physical network there.
- Windows: after the Elarni adapter was disabled, which ends the connection, the next connect failed with
  "vpn_core_failed_2": the helper reused the disabled adapter it keeps between connects. It now replaces it.
- DNS lookups get through when the VPN's UDP session table is full (every platform). A burst of new
  UDP flows, such as the discovery packets Windows sends at each network change, refused every
  lookup for about 10 s; lookups now have up to 128 sessions of their own.
- Windows: a recording build recorded no VPN traffic when the ElarniVPN service started the
  helper (Connect without administrator approval), or when another administrator approved
  it: the helper wrote into that account's profile, which Save recording as ZIP cannot read.
  It now records under the helper's admin-only folder, readable only by the connecting account.
- Windows, Linux and macOS: in the routing editor, opening a collapsed section (Direct DNS,
  Choose DNS by domain, Proxy rules, Block rules) or reopening one showed grey boxes instead of
  its text fields.

## 0.5.0 - 2026-11-07

### Security

- Imported profiles can no longer read the core's environment through a field name
  that only matches under Unicode case folding, such as `addreſs`.
- The core's brotli and zstd decoders are updated (brotli 1.2.7, klauspost/compress 1.20.2): a crafted brotli stream
  could allocate excessive memory (GO-2026-6888) and a crafted zstd frame could crash the core (GO-2026-6865).
  The vendored quic-go forks' own module files now ask for the same versions.

### Added

- Windows and Linux: Settings › Connection › Connection mode picks System-wide VPN (the default),
  System proxy or Local proxy only. System proxy points the Windows proxy setting, or
  GNOME's and KDE Plasma's, at Elarni's HTTP listener while connected and puts the
  previous setting back on disconnect, on quit, and at the next start after a crash.
- macOS: the app warns when the VPN extension that runs is another build's (a leftover
  development build's extension used 6.6 GB).
- Android: a line above the servers on Home says when the subscriptions were last updated and that
  pulling the list down updates them; tapping it updates them too, which TalkBack users need.

### Changed

- A release is built only from a commit whose checks passed, and every main commit
  is checked (a newer push no longer cancels a pending run).
- Windows and Linux: a dropped connection is retried on the selected server without
  automation turned on: after 1, 2, 4, 8, 16 and 30 s, then every minute for as long as
  you stay connected and online. Failover to other servers still needs automation. A
  connection left on when Elarni quits comes back at the next start; Disconnect ends it.
  The On Demand switch, which did nothing there, is gone.
- Windows and Linux: the elevated helper outlives a tunnel that failed or ended by
  itself, so reconnecting asks for administrator approval only once per session.
- Windows and Linux: with automation on, a failed server check is repeated after 10 s
  instead of 30 s, so failover leaves a dead server within about 80 s instead of 120 s.
  Each backup is tried in turn without saving the catalog again or restarting the
  failed server in between; the original comes back only if no backup works.
- iOS: On Demand is on for new installs; a saved choice is kept.
- Android: apps that need no tunnel (bypassed apps going direct, and unlisted apps in
  Only listed mode) stay out of the VPN entirely while lockdown is off, and use the
  system's DNS.
- Android: after a crash the VPN comes back by itself, at most once in 10 minutes, and
  the watchdog also runs while the phone dozes.
- Android: the background refresh job runs hourly, and only when a subscription, route
  list or expiry reminder is due. Server checks run only while traffic flows.
- The bundled Xray core is upstream main a52db8f6 (v26.10.31 and later), which adds Windows
  TUN fixes (starting with IPv6 disabled, reusing an adapter by name), MPTCP off on listeners
  by default, and fixes for QUIC sniffing and HTTP 100-continue.

### Fixed

- A TLS, REALITY or HTTPUpgrade handshake that DPI swallows gives up after 16 s and frees
  its socket; a failed handshake no longer leaks it. A refused or timed-out server fails
  at once instead of after five tries.
- DNS: a lookup that fails is answered with SERVFAIL, so apps try again at once instead
  of waiting out their own timeout; a lost query is sent again after 1 s; a remote
  resolver that stalls gives up after 2 s, and expired answers are served while they
  refresh. A DoH server that stalls while connecting gives up after 16 s instead of
  holding every lookup. A plain-UDP remote resolver now goes through the proxy, and
  `+local` resolvers in imported profiles, which bypassed the tunnel, are dropped.
- After a network change, pooled XHTTP, gRPC, Hysteria, TUIC and DNS connections are
  checked and only dead ones are closed; new connections use the new network (Windows,
  Linux, Android, macOS, iOS). Android and macOS keep the core running instead of
  restarting it, and Linux and Windows also do this after sleep.
- XHTTP/3: a failed dial no longer leaks its socket, more than 100 sessions no longer
  stall for 16 s, and closing the first app no longer aborts the shared dial.
- Routing with IP rules uses the connection's own address instead of looking its domain
  up again.
- Android: an error in a background task no longer ends the app and its VPN; a failed
  save no longer undoes a connect.
- Android: in Only listed mode, a connection whose app Android cannot identify stays in
  the tunnel.
- Android: the Quick Settings tile shows a VPN paused for sleep as off.
- iOS and macOS: a stalled core is restarted after two failed checks, and packet writes
  refused while settings change no longer end the tunnel.
- Linux: a failed nft cleanup keeps the marker, so Repair VPN setup still finds the
  leftover redirect.
- Windows: a DNS setting that could not be restored on disconnect is reported, and
  retried.
- Testing many servers at once no longer fails a server now and then: two probes could be
  given the same local port and fail each other's login (Windows, Linux, macOS, Android).
- A server now gets the site name it routes by, not the app's IP, when the app
  connected to a real address (for example over IPv6). Servers that send YouTube
  through an IPv4-only exit no longer stall YouTube on IPv6 phones. Direct
  connections, UDP and names the profile pins in its DNS hosts still use the
  app's address.
- With a Fake DNS routing profile, refreshing a subscription, geofile or managed list
  while connected asked to approve a "private network address", or failed, and paused
  automatic refresh, on every platform. Downloads now look such names up over DNS over
  TLS through the VPN, which does not answer with Fake DNS addresses, and fall back to
  the VPN's own DNS where DNS over TLS is blocked.
- Windows, Linux: with a Fake DNS routing profile, testing servers other than the
  connected one failed while connected, so failover found no backup.
- Android: the update notice never appeared; the daily check failed before it fetched.
- Windows, Linux: SRV, TXT and other lookups that aren't for addresses went to a
  resolver the routing profile lists for its own domains only.
- SRV, TXT and other lookups that aren't for addresses, asked over TCP through the VPN,
  came back truncated whenever the answer passed 512 bytes, so they failed; they now
  stay on TCP.
- macOS: while connected, Direct TCP showed 1-5 ms for every server, dead ones too, because
  the tunnel itself answered; it now tests outside the tunnel. Tests the tunnel cannot run
  while connected (Direct TCP and other servers' proxy tests with Include all networks
  on, and other servers' "Current rules / active tunnel" tests) are marked Skipped with a
  hint to disconnect, instead of failing and marking the server Unavailable.
- Windows and Linux: importing a subscription or routing profile while the VPN is
  connected with FakeDNS no longer fails with "This source needs approval before it
  can be contacted." Geofile and managed-list downloads now resolve and connect
  through the VPN's SOCKS listener, as on Android.

### Performance

- iOS and macOS: faster downloads through the packet tunnel (TCP window scaling, one copy
  per segment, half the ACKs); a burst of packets waits up to 10 ms instead of being
  dropped; DNS sessions end with their answers.
- Fewer copies and allocations per UDP datagram in the TUN stack, Hysteria, TUIC and
  WireGuard; TUIC uses batched reads and GSO.
- Idle UDP flows in the TUN stack end after 2 minutes instead of 5; a full table refuses
  new connections before their handshake; near the memory limit, idle sessions are
  replaced first.
- Hysteria keeps its connection alive (10 s keepalive) and shares one dial between
  flows; XHTTP gives new flows a connection that is already up.
- Android: the core's DNS answers are cached for their TTL, routing data is not read
  again for the same route, and logs are capped at 200 lines a second.
- The core returns memory after light traffic at most once a minute instead of every
  5 s, and still at once after a large download or when many connections close.
- iOS: lower peak memory for XHTTP and QUIC downloads, and the core is not compiled
  again for a restart with the same settings.
- Desktop: no window rebuilds or catalog saves on unchanged state, direct TCP tests use
  sockets instead of a core per server, and the helper returns memory after each start.
- Windows: reconnecting with unchanged routing data no longer sends and checks the
  geofiles again (80 ms and about 100 MB of copies for 18 MiB of geodata).
- Linux: per-app routing looks up the owners of a burst of new connections in one
  pass (64 connections: 9 ms instead of 36 ms).
- Windows: an idle direct UDP flow no longer holds a 64 KiB read buffer.
- A connection closes as soon as the app closes it, instead of up to a second later (Xray's default `downlinkOnly`). A
  burst of short connections no longer holds hundreds of TLS sessions: on the test phone the app peaked at 88 instead
  of 231 MB during 2000 short connections and opened 739 instead of 633 per second; the desktop daemon peaked at 82
  instead of 225 MB on Linux and 102 instead of 265 MB on Windows. An app that half-closes a connection and then waits
  for the reply loses the reply, as with the mihomo-based clients; a profile's own `policy` value still wins.
- Android: the core now uses the CPU's AES and carry-less multiply instructions. Go, loaded as a library, could not
  read the CPU features in a release build, so TLS ran on software AES. On a Snapdragon 8 Gen 3 test phone one
  download now runs at 136 instead of 37 MB/s and eight at 907 instead of 201, at a third to a seventh of the CPU
  per MB; uploads and new connections per second gain too.
- UDP in VPN (TUN) mode: datagrams waiting on a flow go to the server together, packed into
  full TLS records, instead of one record and one write each. On the test phone UDP bulk
  rises from 4.5 to 10.7 MB/s at half the CPU per MB; on desktops by 8-12%.
- The VPN (TUN) mode on Windows, Linux and Android runs on Xray's own TCP/IP stack instead
  of gVisor. One connection downloads up to 2.5x faster (VLESS: 2.75 vs 1.08 GB/s on the
  test host) at less than half the CPU per MB, now level with upstream Xray on every protocol.
- Linux: without a GPU (virtual machines, remote desktops), the window draws with Flutter's own
  CPU renderer instead of Mesa's software OpenGL: 80 MB less memory and a quarter of the CPU
  while it repaints. Set `FLUTTER_LINUX_RENDERER=opengl` to keep OpenGL.
- The window no longer redraws every second while the connection status is unavailable.
- Windows and Linux: a connected but idle VPN uses about 40% less CPU (Linux test namespace: 65 instead of 112 ms
  per minute across the app and its core processes). The core passes its traffic counters on only when they change,
  or every 10 s, instead of every second through each process; it reads its heap size without pausing the program;
  and the Linux network check reads link state without a thread handoff.
- Android: the 2-second status refresh no longer asks the system for home screen widgets each time.
- Android: the 2-second status refresh no longer reloads every settings title, and the screen's clock stops while
  the app is in the background. Idle CPU in the minute after heavy traffic drops by about a fifth (1020 vs 1260 ms
  per minute on the test phone); settled idle is unchanged.

## 0.4.1 - 2026-11-04

### Changed

- Release files are built on GitHub-hosted runners and come with build provenance
  (`gh attestation verify`) and a Sigstore-signed `SHA256SUMS`; see docs/RELEASING.md.
- Android: server tests run while connected, as on desktop. Other servers, direct
  TCP and ICMP are tested outside the tunnel; the selected server's proxy test still
  goes through it. While connected, a test runs 8 servers at a time with a health
  check between them, so failover isn't held up.

### Security

- Android: broadcasts from automation apps such as Tasker must carry the control
  key shown under Settings › General › Let other apps control the connection, as
  the string extra `key`; others are ignored. Existing automations need the key
  added (android/README.md has a Tasker example).
- Imported profiles can no longer name an address or port as `env:NAME`, which made
  the core substitute that local environment variable into server hosts and DNS
  lookups. Names, tags and passwords may still contain `env:`.

### Fixed

- RU routing from runetfreedom's geofiles works again: its geosite.dat passed the
  64 MiB limit. Downloaded geofiles (up to 256 MiB) are streamed to disk and trimmed
  to the categories your rules use; with Reduce routing-data size off, a file over
  64 MiB fails the refresh with a clear message.
- Android: connections no longer hang when the phone reuses a port the tunnel still
  held in TIME_WAIT, and connection bursts are no longer refused.
- iOS: the VPN recovers after a switch between Wi-Fi and cellular, or between two Wi-Fi
  networks. The tunnel restarts its core in place once the new network has held for
  6 s, and retries if the new network isn't ready yet.
- iOS and macOS: the packet tunnel no longer keeps every packet it wrote until it stops;
  download-heavy apps made iOS end the VPN at its 50 MB memory limit.
- iOS: diagnostics report the memory footprint iOS counts against that limit.
- A subscription refresh no longer reconnects when the active server changed only its
  REALITY shortId or SNI, which 3x-ui picks at random on every fetch. The stored values
  are kept for 24 hours after the server's last connect, so a shortId the panel retired
  is replaced at the next refresh after that.
- A JSON or Clash profile whose server has no port, or a host name with a space,
  slash or colon, is refused on import, as its share link already was, instead of
  failing to connect.
- `elarni-cli` without `HOME` (a systemd unit without `User=`) finds the root service's
  socket, or `$XDG_RUNTIME_DIR/elarni/control.sock`, instead of failing.
- Linux: a tunnel killed while it started can no longer leave a DNS redirect that the
  next start does not detect.
- Routing: a `regexp:` rule Go cannot compile (unbalanced groups or classes, a trailing
  backslash, a repeat with nothing to repeat, nested repeats, unknown escapes) is
  rejected on import instead of failing at connect.
- Windows: upgrading an install that never had the ElarniVPN service sets it up for the
  installing account once, so connecting stops asking for administrator approval. An
  install run as SYSTEM (Intune, SCCM) sets up no account.

### Performance

- Faster downloads and connects on every platform: WireGuard, QUIC (Hysteria2,
  XHTTP/3, TUIC), XHTTP and the TUN stacks got larger, bounded buffers, and a stalled
  QUIC stream no longer freezes its connection. On Windows, 8-stream downloads are
  1.2-3.3x faster on 22 of 24 protocols, with 1.5-5x less CPU per GB.
- Server-first protocols (SMTP, IMAP, MySQL, SSH and others) get their greeting at
  once instead of after up to 0.7 s.
- Per-app routing looks a connection's process up once instead of once per rule.
- The core returns memory after traffic stops (desktop) and returns freed memory to
  the system (Android).

## 0.4.0 - 2026-11-01

First open-source release.

### Added

- Release builds check this repository's releases once a day and show a notice
  when a newer version exists (Settings › General › Update feed; nothing installs).
- Settings › General has a language choice, English or Russian.
- Android: the Quick Settings tile names the connected server.

### Changed

- Every platform uses Elarni identifiers: `com.elarni.*` bundle, package and
  desktop IDs, `Elarni` data folders and registry keys, `/var/lib/elarni`,
  `/run/elarni` and `/etc/elarni` for the Linux service, and `elarni://` links.
  Builds from before this change are a separate app: remove the earlier build
  first, then install this release and re-add subscriptions. On Windows, the
  earlier build's uninstaller, or `elarni.exe --unregister` for its portable ZIP,
  releases its link handlers and login item. Backups from earlier builds cannot
  be restored.
- Desktop: per-app routing is its own Routing tab, subscriptions open with their
  servers expanded, and the Home server card is shorter.
- Connection recording is left out of release builds; it ships only in builds
  made with `ELARNI_RECORDING`.

### Security

- tokio 1.53.3 (RUSTSEC-2026-0386) and golang.org/x/net 0.62.0 (GO-2026-6860,
  GO-2026-6866).
