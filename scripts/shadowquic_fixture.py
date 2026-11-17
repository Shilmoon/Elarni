"""Disposable ShadowQUIC peer for the tunnel acceptance checks.

Runs the test server built from native/elarni-shadowquic (`cargo build --locked -p
elarni-shadowquic --examples`, or ELARNI_SHADOWQUIC_SERVER); never configures TUN, routes or
DNS on the host. The server's requests leave directly, or through a SOCKS5 server the check
runs, which decides where the check's made-up destinations lead.
"""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time


def shadowquic_outbound(address, port):
    """The Xray outbound for the peer: the test server's one user and server name."""
    return {'tag': 'proxy', 'protocol': 'shadowquic', 'settings': {
        'address': address, 'port': port, 'username': 'user', 'password': 'pass',
        'serverName': 'localhost', 'alpn': ['h3'], 'zeroRtt': False, 'udpOverStream': True}}


@contextmanager
def shadowquic_peer(root, listen_host='127.0.0.1', socks=None):
    name = 'shadowquic-test-server' + ('.exe' if os.name == 'nt' else '')
    binary = Path(os.environ.get('ELARNI_SHADOWQUIC_SERVER', root / 'target/debug/examples' / name))
    if not binary.is_file():
        raise RuntimeError('Build the ShadowQUIC test server first: cargo build --locked -p elarni-shadowquic --examples')
    host = f'[{listen_host}]' if ':' in listen_host else listen_host
    process = subprocess.Popen([str(binary), host + ':0', *([socks] if socks else [])],
                               stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    try:
        line = process.stdout.readline()
        if not line:
            raise RuntimeError('Disposable ShadowQUIC peer did not start.')
        yield json.loads(line)['port']
    finally:
        # The server ends when its input does.
        process.stdin.close()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill(); process.wait(timeout=3)


@contextmanager
def redirecting_socks(root, tcp_port, udp_port):
    """A loopback SOCKS5 server that ends every TCP and UDP request at these loopback ports: the peer's way
    out where the destinations are made up (Android). It is the TUIC fixture's sing-box (docs/TESTING.md)."""
    binary = Path(os.environ.get('ELARNI_TUIC_SERVER', root / '.build/tuic/tools/sing-box'))
    if not binary.is_file():
        raise RuntimeError('Build the pinned sing-box TUIC fixture described in docs/TESTING.md first.')
    with tempfile.TemporaryDirectory(prefix='elarni-shadowquic-') as temporary:
        directory = Path(temporary)
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        path = directory / 'socks.json'
        path.write_text(json.dumps({'log': {'level': 'info'},
            'inbounds': [{'type': 'socks', 'listen': '127.0.0.1', 'listen_port': port}],
            'outbounds': [{'type': 'direct', 'tag': 'fixture'}],
            'route': {'rules': [{'network': network, 'action': 'route', 'outbound': 'fixture',
                                 'override_address': '127.0.0.1', 'override_port': target}
                                for network, target in [('tcp', tcp_port), ('udp', udp_port)]]}}))
        with (directory / 'socks.log').open('w+') as log:
            process = subprocess.Popen([str(binary), 'run', '-c', str(path)], stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 5
                while True:
                    log.seek(0)
                    if 'sing-box started' in log.read():
                        break
                    if process.poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError('The ShadowQUIC peer\'s SOCKS server did not start.')
                    time.sleep(0.02)
                yield '127.0.0.1:' + str(port)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill(); process.wait(timeout=3)
