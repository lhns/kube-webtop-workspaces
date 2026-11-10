#!/usr/bin/env python3
"""Counts as a Selkies web client for the server: a websocket to /api/websockets with the primary
display's settings (webtop-nix tests/power.nix does the same with aiohttp). Stdlib only; stays
connected until killed. Usage: ws-client.py <port>"""
import base64
import json
import os
import socket
import struct
import sys


def frame(opcode, payload):
    mask = os.urandom(4)
    n = len(payload)
    head = bytes([0x80 | opcode]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack(">H", n))
    return head + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload))


def read(s, n):
    buf = b""
    while len(buf) < n:
        chunk = s.recv(n - len(buf))
        if not chunk:
            sys.exit("closed by the server")
        buf += chunk
    return buf


s = socket.create_connection(("127.0.0.1", int(sys.argv[1])))
s.sendall((
    "GET /api/websockets HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
    f"Sec-WebSocket-Key: {base64.b64encode(os.urandom(16)).decode()}\r\nSec-WebSocket-Version: 13\r\n\r\n"
).encode())
resp = b""
while b"\r\n\r\n" not in resp:
    resp += read(s, 1)
if b" 101 " not in resp.split(b"\r\n")[0]:
    sys.exit(f"no upgrade: {resp.decode(errors='replace')}")
s.sendall(frame(1, ("SETTINGS," + json.dumps({
    "displayId": "primary", "manual_resolution": False, "useCssScaling": True, "displayScale": 1,
    "initialClientWidth": 1280, "initialClientHeight": 720, "audioRedundancy": True})).encode()))
while True:
    b1, b2 = read(s, 2)
    n = b2 & 0x7F
    if n == 126:
        n = struct.unpack(">H", read(s, 2))[0]
    elif n == 127:
        n = struct.unpack(">Q", read(s, 8))[0]
    data = read(s, n)
    if b1 & 0x0F == 9:
        s.sendall(frame(10, data))
    elif b1 & 0x0F == 8:
        sys.exit("closed by the server")
