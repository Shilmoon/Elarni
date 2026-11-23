# Headless Elarni on Linux

`elarni-cli` runs Elarni on a server without a display or desktop login. Commands
and the method reference are in [the control guide](../docs/HEADLESS.md).

## Boot service

Install `gnome-keyring`, `libsecret-tools` and `dbus-x11`, create the keyring
password, then enable the service (the package installs it disabled):

```sh
sudo install -d -m 700 /etc/elarni
sudo sh -c 'umask 077; test ! -e /etc/elarni/keyring-password && openssl rand -hex 32 > /etc/elarni/keyring-password'
sudo systemctl enable --now elarni.service
```

The service runs as root with a private Secret Service; profiles are encrypted
under `/var/lib/elarni`. Keep the password, keyring and catalog together when
moving to another machine. Starting the service doesn't connect until you set a
profile up.

A full-tunnel VPN takes the replies of incoming connections too, so SSH and the
ports the server publishes stop answering from outside its network. Before the
first `connect` on a remote server, turn on the setting that answers them outside
the tunnel:

```sh
echo '{"answerInbound":true}' > /tmp/inbound.json
sudo elarni-cli settings /tmp/inbound.json
```

With it, a connection that reaches the server from outside (SSH, a web server, a
published container port, a UDP service) is answered through the network it came
from, also under Block traffic without VPN, and sessions that were open before
the tunnel keep working. Everything the server opens itself still goes through
the VPN. It is off by default: on a desktop it would let a program that accepts
connections, such as a torrent client, answer them outside the VPN. It needs the
kernel's reverse-path filter in loose mode or off (`rp_filter` 2 or 0, the
default on Debian, Ubuntu and Fedora).

`journalctl -u elarni` shows the service's notices and connection states, and
`sudo elarni-cli call readLogs` prints the core's log. `sudo elarni-cli shutdown`
stops the service until `systemctl start elarni` or the next boot;
`systemctl restart elarni` restarts it. Either keeps the connection intent. A
crash, SIGKILL or a keyring failure starts it again after five seconds.

The service does not update itself. Install the new package with
`sudo apt install ./elarni_*_amd64.deb`; a running service restarts on it.

## Example

```sh
sudo elarni-cli import /private/subscription-url.txt
sudo elarni-cli profiles
sudo elarni-cli failover 'preferred profile' 'backup profile'
sudo elarni-cli connect
sudo elarni-cli status
```

- `settings` with `{"xrayTunEnabled":false}` runs a local SOCKS/HTTP proxy instead
  of a system VPN. Its listeners require a login: `call inboundCredentials`
  prints it.
- `failover` keeps a healthy connection and switches to the backup when it fails.
  A third argument names a JSON file of custom check URLs.
- After failed retries the daemon waits 60 seconds and tries again. It reconnects
  after a restart unless you ran `disconnect`.

## Private user daemon

```sh
elarni-cli --socket /private/run/control.sock daemon \
  --core /path/elarni-core --domain /path/libelarni_domain.so
```

It needs an unlocked Secret Service in the session (or `--keyring-password-file`
for a dedicated service account). A system VPN still needs root or polkit.
