#!/usr/bin/env python3
"""Tiny HTTP echo server for the VM end-to-end tests (scripts/vm-e2e.sh).

It reports the client's source address, which is how the tests prove where
a request came out: a request that went through the test proxy fleet
arrives from the fleet's own address, a direct one from the VM's address.

    GET /                 -> JSON {"ip","port","addr","host","path"}
    GET /stream?secs=N    -> one chunked line every 200 ms for N seconds
                             (a long-lived connection for switch tests)
    GET /blob?mb=N        -> N MiB of zeros (throughput load)

usage: vm-e2e-echo.py <bind-ip> <port>
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


class Echo(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):  # quiet
        pass

    def do_GET(self):
        u = urlparse(self.path)
        q = parse_qs(u.query)
        if u.path == "/stream":
            secs = min(int(q.get("secs", ["10"])[0]), 600)
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Transfer-Encoding", "chunked")
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            end = time.monotonic() + secs
            n = 0
            try:
                while time.monotonic() < end:
                    line = f"{n} {self.client_address[0]}:{self.client_address[1]} {time.time():.3f}\n".encode()
                    self.wfile.write(b"%x\r\n%s\r\n" % (len(line), line))
                    self.wfile.flush()
                    n += 1
                    time.sleep(0.2)
                self.wfile.write(b"0\r\n\r\n")
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        if u.path == "/blob":
            mb = min(int(q.get("mb", ["8"])[0]), 512)
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(mb << 20))
            self.end_headers()
            chunk = b"\0" * (1 << 20)
            try:
                for _ in range(mb):
                    self.wfile.write(chunk)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        ip, port = self.client_address[0], self.client_address[1]
        body = json.dumps({"ip": ip, "port": port, "addr": f"{ip}:{port}", "host": self.headers.get("Host", ""), "path": self.path}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    srv = ThreadingHTTPServer((sys.argv[1], int(sys.argv[2])), Echo)
    srv.daemon_threads = True
    srv.serve_forever()


if __name__ == "__main__":
    main()
