#!/usr/bin/env python3
"""Run subscription checks against an ephemeral, loopback-only HTTP fixture."""
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import subprocess
import sys
import threading
from urllib.parse import parse_qs, urlsplit


class Handler(BaseHTTPRequestHandler):
    mode = "baseline"

    def log_message(self, *_):
        pass

    def do_GET(self):
        target = urlsplit(self.path)
        if target.path == "/mode":
            Handler.mode = parse_qs(target.query)["value"][0]
            body = b"ok"
        elif target.path == "/binary-resource":
            body = bytes([0, 255, 128, 7]) * 1024
        elif target.path == "/redirect" or target.path.startswith("/long/"):
            self.send_response(302)
            step = int(target.path.rsplit("/", 1)[-1]) if target.path.startswith("/long/") else 4
            self.send_header("Location", f"/long/{step + 1}" if step < 4 else "/feed")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        elif target.path == "/backup":
            body = (b"vless://00000000-0000-4000-8000-000000000001@192.0.2.1:443"
                    b"?encryption=none&security=tls&sni=example.com&type=tcp#Backup\n")
        elif target.path == "/feed":
            version = Handler.mode
            if version.startswith("status_"):
                self.send_response(int(version[len("status_"):]))
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if version == "device_limit":
                self.send_response(404)
                self.send_header("X-Hwid-Max-Devices-Reached", "true")
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if not self.headers.get("X-HWID") or not self.headers.get("X-Device-OS"):
                self.send_response(404)
                self.send_header("X-Hwid-Not-Supported", "true")
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if version == "user_agent" and self.headers.get("User-Agent") != "FixtureClient/1.0":
                self.send_response(403)
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if version == "oversized":
                self.send_response(200)
                self.send_header("Content-Length", str(4 * 1024 * 1024 + 1))
                self.end_headers()
                return
            if parse_qs(target.query).get("format") == ["info"]:
                base = "http://" + self.headers["Host"]
                body = json.dumps({"sId": "fixture", "enabled": True, "isOnline": False,
                                   "uploadByte": 1024, "downloadByte": 2048, "totalByte": 8192,
                                   "subUrl": base + "/feed", "subJsonUrl": base + "/feed?kind=json",
                                   "subClashUrl": base + "/feed?kind=clash", "announce": "Fixture announcement"}).encode()
            elif version == "malformed":
                body = b"vless://malformed"
            elif version == "three_servers":
                body = "".join(f"vless://00000000-0000-4000-8000-000000000001@192.0.2.1:{443 + i}"
                               f"?encryption=none&security=tls&sni=example.com&type=tcp#{name}\n"
                               for i, name in enumerate(["First", "Second", "Third"])).encode()
            elif version.startswith("reality_"):
                # 3x-ui sends one of the server's shortIds and serverNames per fetch; reality_fp changes the fingerprint.
                sid, sni, fp = {"reality_c2d3": ("c2d3", "www.example.com", "chrome"),
                                "reality_fp": ("e4f5", "example.org", "firefox")}.get(version, (version[len("reality_"):], "example.com", "chrome"))
                body = ("vless://00000000-0000-4000-8000-000000000001@192.0.2.1:443?encryption=none&security=reality"
                        f"&type=tcp&fp={fp}&pbk=FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs&sni={sni}&sid={sid}#Test\n").encode()
            elif version.startswith("panel_"):
                # A 3x-ui panel picks the REALITY sid and sni per fetch and writes the traffic left into the remark.
                # DE-1 is one host listed twice; panel_fewer lists it once.
                sid, sni, left = {"panel_first": ("a1b2", "www.example.com", "9.52GB"),
                                  "panel_sid": ("e5f6", "www.example.com", "9.47GB"),
                                  "panel_sni": ("e5f6", "cdn.example.org", "9.41GB"),
                                  "panel_all": ("0c1d", "www.example.net", "9.30GB"),
                                  "panel_fewer": ("7a8b", "www.example.com", "9.20GB")}[version]
                servers = [("DE-1", 11), ("DE-1", 11), ("NL-1", 12)][1 if version == "panel_fewer" else 0:]
                body = "".join(f"vless://00000000-0000-4000-8000-0000000000{host}@192.0.2.{host}:443?encryption=none"
                               "&security=reality&type=tcp&fp=chrome&pbk=FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs"
                               f"&sni={sni}&sid={sid}0{i}#{name}%20{left}\n" for i, (name, host) in enumerate(servers)).encode()
            elif version == "provider_messages":
                # Three lines that carry a provider's messages around two servers. This feed is served from
                # this machine, so all five stay servers.
                body = ("socks://127.0.0.1:1080#📊 10GB left\n"
                        "vless://00000000-0000-4000-8000-000000000001@de.example:443?encryption=none&security=reality"
                        "&type=tcp&fp=chrome&pbk=FCNsCTRerqst97Y-_Pc32LnnBu0g_hw-LhJYlaeiEVs&sni=www.example.com&sid=0a1b#DE-1\n"
                        "socks://192.0.2.1:1#Device limit reached\n"
                        "trojan://fixture@nl.example:443?security=tls&sni=nl.example#NL-1\n"
                        "trojan://1@1.1.1.1#✖ Package Ended\n").encode()
            elif version in ("full_profile", "system_dns"):
                body = json.dumps({"remarks": "Test", **({"dns": {"servers": ["localhost"]}} if version == "system_dns" else {}), "outbounds": [{
                    "tag": "proxy", "protocol": "vless",
                    "settings": {"vnext": [{"address": "192.0.2.1", "port": 9443,
                        "users": [{"id": "00000000-0000-4000-8000-000000000001", "encryption": "none"}]}]},
                    "streamSettings": {"security": "tls", "tlsSettings": {"serverName": "example.com"}}
                }]}).encode()
            else:
                port = 8443 if version == "updated" else 443
                body = (f"vless://00000000-0000-4000-8000-000000000001@192.0.2.1:{port}"
                        "?encryption=none&security=tls&sni=example.com&type=tcp#Test\n").encode()
                if version in ("dual_routing", "body_routing", "base64_dual_routing", "bad_header_routing", "addressed_routing"):
                    routing = json.dumps({"Name": "Body route", "RemoteDNSType": "DoU",
                                          "RemoteDNSIP": "9.9.9.9", "DomesticDNSType": "DoU",
                                          "DomesticDNSIP": "8.8.8.8",
                                          "DirectSites": ["full:body.example"]}).encode()
                    body += b"incy://routing/onadd/" + base64.b64encode(routing) + b"\n"
                    if version == "base64_dual_routing":
                        body = base64.b64encode(body)
                elif version == "bad_body_routing":
                    body += b"incy://routing/onadd/%%%\n"
                elif version == "body_routing_off":
                    body += b"incy://routing/off\n"
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        if target.path == "/feed":
            profile = json.dumps({"Name": Handler.mode, "RemoteDNSType": "DoU", "RemoteDNSIP": "1.1.1.1",
                                  "DomesticDNSType": "DoU", "DomesticDNSIP": "8.8.8.8"}, separators=(",", ":"))
            if Handler.mode == "bad_header_routing":
                self.send_header("routing", "happ://routing/unsupported")
            elif Handler.mode == "addressed_routing":
                self.send_header("routing", "elarni://routing/onadd/" + base64.b64encode(profile.encode()).decode())
            elif Handler.mode != "body_routing":
                self.send_header("routing", "happ://routing/onadd/" + base64.b64encode(profile.encode()).decode())
            if Handler.mode != "untitled":
                self.send_header("Profile-Title", "Updated provider" if Handler.mode == "renamed" else "Fixture")
            self.send_header("Subscription-Userinfo", "upload=1024; download=2048; total=8192; expire=2000000000")
            self.send_header("Announce", "base64:" + base64.b64encode("Fixture announcement".encode()).decode())
            self.send_header("Profile-Update-Interval", "6")
            self.send_header("X-Hwid-Active", "true")
            self.send_header("Support-Url", "https://provider.example/support?fixture=token")
            self.send_header("Profile-Web-Page-Url", "https://provider.example/account?fixture=token")
            self.send_header("Banner-Text", "Free provider notice")
            self.send_header("Banner-Button-Text", "View notice")
            self.send_header("Banner-Button-Url", "https://provider.example/notice?fixture=token")
            if Handler.mode == "fallback":
                self.send_header("Fallback-Url", "http://" + self.headers["Host"] + "/backup?fallback=token")
            if Handler.mode == "migration":
                self.send_header("New-Url", "http://" + self.headers["Host"] + "/feed?migrated=token")
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass


if __name__ == "__main__":
    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        env = dict(os.environ, ELARNI_TEST_FIXTURE=f"http://127.0.0.1:{server.server_port}")
        try:
            result = subprocess.run(sys.argv[1:], env=env, timeout=90)
        except subprocess.TimeoutExpired:
            print("FAIL subscription checks timed out", file=sys.stderr)
            result = None
        finally:
            server.shutdown()
            thread.join()
        sys.exit(result.returncode if result else 1)
