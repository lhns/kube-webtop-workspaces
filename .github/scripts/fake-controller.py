#!/usr/bin/env python3
"""Stands in for the controller's report API: acks every POST with 204 and appends the body to a file."""
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open(sys.argv[2], "ab") as f:
            f.write(self.path.encode() + b" " + body + b"\n")
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):
        pass


HTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()
