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
import subprocess


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
