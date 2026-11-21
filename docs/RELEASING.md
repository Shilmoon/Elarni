# Releasing

Releases are direct downloads: an unsigned Windows ZIP and MSI, a Linux `.deb`,
AppImage and portable archive, a signed Android APK and the source archives. Their
integrity comes from build provenance and a signed `SHA256SUMS`, not from platform
code signing. No macOS or iOS build is published; the macOS and Windows signing
commands below are for builds signed by hand.

## Release flow

Release files are built by `.github/workflows/release.yml` on GitHub-hosted runners, which start without
any cache from earlier builds. The self-hosted CI runners also run pull request and Dependabot code, so
nothing they build is published.

1. Run the workflow with stage `build` on the commit to release. It refuses a commit without a
   successful "checks passed" from Development checks, then builds the Linux packages, the Windows ZIP
   and MSI, the source archives and the unsigned APK, and records build provenance for each file.
2. On the release machine, which holds the Android signing key and the release key, sign the APK,
   write `SHA256SUMS`, sign it (below) and upload a draft release at the run's commit with every file
   of the run plus `SHA256SUMS` and `SHA256SUMS.sig`. The maintainer's scripts for this step
   (`assemble.sh`, `gh_release.py`) live on that machine, not in this repository.
3. Run the workflow with stage `publish` and the tag. It publishes only a draft that is one build's
   whole set of files (`scripts/release_draft.py`): the draft's target is a commit; every file is in
   `SHA256SUMS` and matches it; every package has build provenance from this workflow at that commit
   and from the same run; no file that run attested is left out; the tag names the version it built;
   and the signed APK holds, entry by entry, what the run's unsigned APK holds, plus its signature.
   Files may be renamed in step 2, since they are matched by digest. Then it signs `SHA256SUMS`
   (Sigstore, keyless) and publishes the release. The unsigned APK is read from the build run's
   artifacts, which are kept for 30 days: publish within that time.

Users can check a download:

```sh
gh attestation verify Elarni-0.4.1-12-x86_64.AppImage --repo Shilmoon/elarni
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/Shilmoon/elarni/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c SHA256SUMS
```

## Updates

Desktop and Android release builds read
`https://api.github.com/repos/Shilmoon/elarni/releases/latest` once a day (hourly while it
cannot be read) and show a notice when it names a newer version; pre-release tags never do.
Settings › General › Update feed changes it to another HTTPS address, serving a GitHub
`releases/latest` reply or `{"version": "0.4.1", "url": "https://…"}`, or clears it to turn the
check off; debug builds start with it off. The default feed needs the repository to be public.
A headless daemon (`elarni-cli daemon`, the Linux boot service) never checks; a server is
updated with its package ([HEADLESS.md](HEADLESS.md)).

An install from the MSI, the `.deb`, the AppImage or the APK also replaces itself with the
release the feed names (Settings › General › Install updates automatically, on by default). It
downloads the package in the background and installs it only when that asks for nothing and
interrupts nothing: Windows at the app's next start, through the ElarniVPN service; the AppImage
in place, used from its next start; Android while the VPN is off, once the user has allowed
Elarni to install apps. Otherwise the Home notice offers Install, behind the consent prompt,
the administrator password or Android's confirmation. A connected tunnel is never cut for an
update the user did not start. The portable ZIP and archive, macOS and iPhone keep the notice
with the link. The feed must list the release's files: a GitHub reply does, and the short form
may carry `"assets": [{"name": "…", "url": "https://…"}]`.

What may install is decided by the release key, not by the feed or the server it names. The
installers run with more rights than the app (SYSTEM, root), so they copy the download to a
place only they can write and accept it only when

- `SHA256SUMS.sig` is the release key's Ed25519 signature of `SHA256SUMS` (the public half is
  in `Vendor/libXray/update/update.go`),
- the package has the digest `SHA256SUMS` lists, under the name the packagers give this form, and
- the version in that name is newer than the installed build.

Android relies on the system instead, which installs only an APK signed like the installed one
and never an older one.

Sign `SHA256SUMS` after the last file is in it; a release without `SHA256SUMS.sig` is refused by
the publish stage, and no installed build would update to it:

```sh
cd Vendor/libXray
go run ./cmd/elarni-sign-update -key ~/.config/elarni-release/update-signing-key /path/SHA256SUMS
```

The tool refuses a key the build does not trust. The key is a 32-byte seed in a file only its
owner can read; keep a copy offline. Losing it means installed builds stay on the notice until
their users install a release by hand that trusts a new key. To replace it, generate one
(`-generate FILE`), add its public half to `keys` in `update.go` and keep signing with the old
key until builds without the new one are out of use. Never sign a test package with it: a
signed package of a made-up high version would install over every older build.

Builds that test updates against their own releases add a key at link time
(`-ldflags "-X github.com/xtls/libxray/update.extraKey=<base64 public key>"`); release builds
never set it.

## Before you package

1. Bump `version` in `pubspec.yaml`; never reuse a published version.
2. Build with the pinned toolchains (`mise.toml`, `rust-toolchain.toml`) from a
   clean checkout, using `lib/main.dart`.
3. Run the checks in [TESTING.md](TESTING.md), including
   `python scripts/check-server-cores` on each OS.

Each packager refuses a non-empty output directory and writes
`release-manifest.json`, `project-source-manifest.json` and `SHA256SUMS`.

Source archives for the GPL obligations:

```sh
python3 scripts/package-source --output .build/release-source
python3 scripts/package-dependencies --output .build/release-dependency-source
```

## macOS

Needs a Developer ID identity with the app's Network Extension, App Group and
Keychain entitlements, and a `notarytool` keychain profile.

```sh
flutter build macos --release --config-only --target=lib/main.dart
xcodebuild -workspace macos/Runner.xcworkspace -scheme Runner -configuration Release \
  -archivePath .build/release/Elarni.xcarchive ARCHS=arm64 archive
python3 scripts/package-macos --archive .build/release/Elarni.xcarchive \
  --export-options /path/ExportOptions.plist --notary-profile PROFILE \
  --output /path/release [--dmg]
```

The script notarizes, staples and runs a Gatekeeper check. `--unsigned` and
`--development` build packages for testing only.

## Windows

```powershell
flutter build windows --release --target=lib/main.dart
python scripts/package-windows --bundle build/windows/x64/runner/Release `
  --output C:\release\new --signtool C:\path\to\signtool.exe `
  --certificate-thumbprint THUMBPRINT --timestamp-url RFC3161_URL --msi
```

The script signs and verifies every EXE and DLL and keeps Wintun's own signature.
`--msi` needs WiX 5 (`dotnet tool install --global wix --version 5.0.2`). The MSI
installs to `%ProgramFiles%\Elarni`; a fresh install also sets up the VPN helper
service for the installing account, so connecting needs no UAC prompt. An upgrade
sets it up too when no service exists and no installer offered it to that account
before (installs upgraded from 0.3.6 or earlier); otherwise upgrades keep each
account's setting (Settings › Connection › Connect without administrator approval).
An install run as SYSTEM (Intune, SCCM) sets up no account. Uninstalling removes the
service.

The portable ZIP asks for administrator approval to install the helper on the
first connection. To remove it: run `elarni.exe --unregister`, then
`elarni-core.exe --uninstall-helper` from an elevated prompt (it also closes a running
Elarni), and delete the folder.

## Linux

On Ubuntu 22.04:

```sh
flutter build linux --release --target=lib/main.dart
python3 scripts/package-linux --output .build/linux-release
```

See [linux/README.md](../linux/README.md) for installation.

## Checklist

- Install the previous release, add data, upgrade, then connect and disconnect.
  Check that settings, servers and routing survive and no routes or DNS changes
  are left behind.
- Check that uninstall keeps user data.
- Record idle cost with `python scripts/measure-idle --seconds 300`.
- Publish the packages, `SHA256SUMS`, source archives, notices and release notes
  together.
