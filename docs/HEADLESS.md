# Headless control

`elarni-cli` controls Elarni on Linux, macOS and Windows. It ships next to the app
(inside `Elarni.app/Contents/MacOS` on macOS); from source, run
`dart run bin/elarni.dart`. Run it as the same user as the app.

## Usage

With the app running, commands work right away:

```sh
elarni-cli status
elarni-cli profiles
elarni-cli connect
elarni-cli disconnect
```

Without the app, `elarni-cli daemon` runs it without a window; stop it with
`elarni-cli shutdown` or Ctrl+C. On macOS and Windows, `--app <path>` picks the
app bundle. A Linux daemon needs no desktop session; see
[linux/HEADLESS.md](../linux/HEADLESS.md) for its keyring and boot service.
Linux and Windows daemons reconnect on the next start unless you ran `disconnect`.
They write what the window would show to stderr, one line each: `notice: <code>`
(for example `connection_stopped` or `scheduled_refresh_failed`), and
`state: <state>` when a notice, a connect or a catalog change leaves the connection
in a new state. `call readLogs` reads the core's log.

A daemon never checks for or installs updates; only the app's window does. Update
a server with its package: on Linux, `sudo apt install ./elarni_*_amd64.deb`.

```sh
elarni-cli import profile.txt          # - reads stdin
elarni-cli select <profile id or name>
elarni-cli probe <profile id or name>
elarni-cli settings [changes.json]     # read, or merge a partial object
elarni-cli refresh <subscription id> [challenge]
elarni-cli failover <primary> <backup>
elarni-cli commands                    # all method names
elarni-cli call <method> [args.json]   # - reads a JSON object from stdin
```

Responses are JSON: `{"success":true,"data":{...}}` or
`{"success":false,"error":"code"}`; the exit status is 0 or 1. A daemon that
cannot start prints the same form with its cause, for example `storage_corrupt`
(the saved catalog cannot be read; it is left untouched), `keyring_unavailable`
or `socket_unavailable`. Pass credentials
in files or stdin, not as arguments. A new subscription source returns
`riskCodes`, the `destinations` it downloads from and a `challenge`; review them, then
`refresh <id> <challenge>`.

## Endpoint

| System | Default endpoint |
|---|---|
| Linux (root service) | `/run/elarni/control.sock` |
| Linux (user) | `$XDG_RUNTIME_DIR/elarni/control.sock` |
| macOS | `~/Library/Containers/com.elarni.mac/Data/.elarni/control.sock` |
| Windows | `%LOCALAPPDATA%\Elarni\control\endpoint.json` |

`--socket <path>` (or `ELARNI_CONTROL_SOCKET` for the app) changes it. Unix uses
a 0600 socket; Windows uses a loopback port with a random token readable only by
your user. Nothing listens on the network.

## Methods

`call` exposes the same operations as the app, with JSON object arguments.

| Methods | Arguments |
|---|---|
| `state`, `subscriptions`, `routingProfiles`, `tunnelSettings`, `automationStatus`, `runtimeMetrics` | None |
| `inboundCredentials` | None; returns the saved SOCKS/HTTP `username` and `password`, or `{}` |
| `readLogs` | Optional `after`, the last `seq` you read; returns the log `level` and the `entries` after it, each with its `seq`. Linux and Windows keep the last 1000 lines and return the newest `seq` as `last`. |
| `connect`, `disconnect`, `repairTunnel` | None |
| `systemInfo`, `routeTable` | None |
| `setLaunchAtLogin` | `enabled` (boolean); controls desktop login registration. Not exposed by the Linux standalone service: manage it with systemd. |
| `importClientProfiles` | `content` (profile text or subscription URL); imports always use supplied text, without a picker/camera. |
| `addSubscription` | `url` |
| `refreshSubscription` | `id`, optional `challenge`, `format` |
| `selectNode` | `id` |
| `setNodeFavorite` | `nodeId`, `favorite` (boolean) |
| `setNodeIPPreference` | `nodeId`, `preference` (`ipv4`, `ipv6`, or null for app default) |
| `selectProfileGroup` | `nodeId`, `group`, `choice` |
| `renameSubscription` | `id`, `name` (null restores its automatic name) |
| `deleteSubscription` | `id` |
| `updateSubscriptionFetchOptions` | `id`, `options` containing `fallbackHosts`, and optional `connectAddress`, `httpHost`, `fragmentLength`, `fragmentInterval` |
| `updateTunnelSettings` | Full settings object from `tunnelSettings`; use the `settings` shortcut for partial updates. |
| `updateInboundCredentials` | `username`, `password` |
| `importRouting` | `content` (routing text) |
| `updateRoutingProfile` | `id`, `content` (routing text) |
| `duplicateRoutingProfile`, `deleteRoutingProfile`, `resetRoutingProfile` | `id` |
| `setRoutingProfileEnabled` | `id`, `enabled` (boolean) |
| `assignRoutingProfile` | `subscriptionId`, `profileId` (null removes the assignment) |
| `refreshRoutingGeofiles` | `id`, optional `file` |
| `configureAutomation` | `schemaVersion:1`, `enabled`, `policy` (`fixed`, `lowestLatency`, `orderedFallback`), `subscriptionID`, `nodeIDs`, `targets`; each target has `id`, `url`, `expectedStatus`, `expectedBody`. Select a member node first. |
| `pingNode` | `nodeId`, `protocol`, `method`, `mode`, `timeout`, `url`, `expectedStatus`, `expectedBody`; same options as the GUI. An ICMP probe may stop and restore a system VPN. |
| `shareClient` | `id`, optional `subscription:true`; returns export text and QR data subject to provider restrictions. |
| `exportCatalogBackup` | `password`; returns encrypted backup in `data.text`. |
| `restoreCatalogBackup` | `password`, `content` (the encrypted text returned above). Disconnect before restoring. |
| `exportEvidence` | Optional `issueCodes` array; returns redacted diagnostics directly. |
| `markRecording` | Recording builds only ([DIAGNOSTICS.md](DIAGNOSTICS.md)). None; notes "problem seen now" in the connection recording (setting `recordDiagnostics`). |
| `exportRecording` | Recording builds only. `destination`: absolute path of a new ZIP, written on the controller's host as the controller's user. macOS ignores it and returns `path`, a ZIP in the sandboxed app's temporary folder. |

Backups and diagnostics are returned in `data.text`; nothing opens a file dialog.
