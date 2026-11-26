# Elarni for iPhone

Native SwiftUI app with a packet-tunnel Network Extension, on the same Go/Xray
engine and Rust profile compiler as the desktop app. Requires iOS 18 or newer.

## Build

Requirements: Xcode 26.4 or newer with the iOS SDK and a Simulator runtime, and
the Go and Rust versions pinned in the repository root.

```sh
rustup target add aarch64-apple-ios aarch64-apple-ios-sim
scripts/build-ios-native iphonesimulator   # or iphoneos for a device
```

Open `ios/Elarni/Elarni.xcodeproj`, choose the **Elarni** scheme and Run. Rebuild
the native libraries after changing Rust, Go or their headers. Set `ELARNI_GO` if
the pinned Go isn't the `go` on `PATH`.

A device build (`iphoneos`) compiles the Rust libraries with the `release` profile,
as the macOS release build does; the simulator uses the quicker `native-dev`
profile, and `scripts/build-ios-native iphoneos --dev` uses it for a device too
(CI's device compile check passes `--dev`).

Go leaves CPU feature detection out of iOS builds until upstream commit 8058a577,
so AES-GCM and SHA-256 would run without the CPU's AES and SHA instructions. The
script applies that commit, kept as `scripts/go-ios-cpu-features.patch`, to copies
of three `internal/cpu` files of the toolchain in `.build/ios/go-overlay/` and
builds the core with `go build -overlay`; the toolchain itself is not changed. Once
the pinned Go has the fix, the script says so and skips the patch; if the patch no
longer applies, the build fails.

The `Shared` folders hold relative symlinks to the macOS sources; keep symlinks
when copying the checkout.

## Test

```sh
scripts/check-ios <simulator-udid>   # from `xcrun simctl list devices available`
```

It runs the native and UI suites with synthetic profiles and loopback traffic; it
never starts a system VPN. Simulator runs need ad-hoc signing for Keychain access.
Shared Swift changes also need `scripts/check-tests swift`.

`scripts/check-ios --build` only compiles the app, its extensions and both test
targets, without a simulator. `scripts/check-changed` runs it for a change under
`ios/` or `macos/`; run the suites on a simulator before a pull request.

Simulator tests don't cover tunnel traffic. On a device, check TCP/UDP and DNS over
IPv4/IPv6, routes, reconnects across Wi-Fi/cellular, background and lock, and the
Control Center and Shortcuts actions. With On Demand on, also stop the VPN with the
iOS VPN switch: the restart must bring a notification whose Turn Off action stops it.

## Signing

To sign with your own team, select it for all three targets and keep these
identifiers consistent across targets, entitlements and the
`ElarniAppGroup`/`ElarniKeychainGroup` Info values:

| Item | Value |
| --- | --- |
| App | `com.elarni.ios.Elarni` |
| Packet tunnel | `com.elarni.ios.Elarni.ElarniPacketTunnel` |
| Control Center widget | `com.elarni.ios.Elarni.ElarniWidgets` |
| App Group | `group.com.elarni.ios` |
| Shared Keychain | `$(AppIdentifierPrefix)com.elarni.ios.shared` |

The app and packet tunnel need the App Group and Network Extension
(`packet-tunnel-provider`) capabilities; the widget needs the App Group.

## Differences from desktop

- Per-app routing isn't available: iOS apps can't see other apps' traffic.
- Background refresh and health checks run only while the app is open; on-demand
  rules handle background reconnects.
- A new install's local proxy listeners require a generated login; set your own
  in Settings › Connection › Inbound credentials. Settings saved before keep their choice.
- The packet tunnel targets a 30 MiB Go heap.
- Backups carry servers, credentials and routing, not app settings.
