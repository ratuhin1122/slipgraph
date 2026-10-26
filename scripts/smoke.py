"""Exercise authenticated planning and worker delivery through the Compose proxy.

Uses only the standard library; secrets are generated in memory and never printed.
The explicit cookie header permits Secure-cookie loopback HTTP smoke testing.
Production browsers still require HTTPS outside trusted localhost development.
"""
import base64
import hashlib
import socket
import ssl
import struct
import uuid
import datetime as dt
import http.cookies
import json
import os
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import urllib.parse

origin = os.environ.get("PUBLIC_ORIGIN", "http://localhost:5173").rstrip("/")
cookies: dict[str, str] = {}


def request(method: str, path: str, body=None, csrf=None, expected=200):
    headers = {"Origin": origin}
    if cookies:
        headers["Cookie"] = "; ".join(f"{key}={value}" for key, value in cookies.items())
    if csrf:
        headers["X-CSRF-Token"] = csrf
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    req = urllib.request.Request(origin + "/api" + path, data=data, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        for value in response.headers.get_all("Set-Cookie", []):
            parsed = http.cookies.SimpleCookie(value)
            for key, cookie in parsed.items():
                assert cookie["secure"] and cookie["httponly"] and cookie["samesite"] == "Lax", "unsafe cookie"
                cookies[key] = cookie.value
        assert response.status == expected, f"{method} {path}: expected {expected}, received {response.status}"
        payload = response.read()
        return json.loads(payload) if payload else None


def mutate(method, path, body=None, expected=200):
    csrf = request("GET", "/auth/csrf")["csrf_token"]
    return request(method, path, body, csrf, expected)


request("GET", "/healthz")
session = mutate("POST", "/auth/signup", {
    "email": f"smoke-{secrets.token_hex(8)}@example.com",
    "password": secrets.token_urlsafe(32),
    "display_name": "Compose smoke", "workspace_name": "Smoke workspace",
})
start = dt.datetime.now(dt.timezone.utc).replace(hour=0, minute=0, second=0, microsecond=0) + dt.timedelta(days=30)
def date(days):
    return (start + dt.timedelta(days=days)).isoformat().replace("+00:00", "Z")
project = mutate("POST", "/projects", {"name": "Smoke launch", "description": "", "start": date(0), "launch_due": date(2)})
def task(title, milestone):
    return mutate("POST", f"/projects/{project['id']}/tasks", {"title": title, "description": "", "status": "todo", "owner_id": None, "start": date(0), "due": date(2), "estimate_minutes": 1440, "milestone": milestone})
first = task("Build", False)
launch = task("Launch", True)
launch = mutate("POST", f"/tasks/{launch['id']}/dependencies", {"version": launch["version"], "blocker_id": first["id"]})
mutate("POST", f"/tasks/{first['id']}/dependencies", {"version": first["version"], "blocker_id": launch["id"]}, expected=409)
mutate("POST", f"/tasks/{first['id']}/delay", {"version": first["version"], "days": 2})
for attempt in range(40):
    risk = request("GET", f"/projects/{project['id']}/risk")
    if not risk["stale"] and risk["assessment"] is not None:
        break
    time.sleep(0.5)
else:
    raise AssertionError("worker did not deliver risk assessment")
assert risk["assessment"]["project_slip_minutes"] == 2880, "wrong downstream impact"
assert risk["assessment"]["launch_slip_minutes"] == 2880, "wrong launch slip"
assert len(risk["assessment"]["critical_path"]) == 2, "missing critical path"
# Verify the production NGINX upgrade path and worker fanout, not only direct API sockets.
channel = mutate("POST", "/channels", {"name": "smoke-chat"})
parsed = urllib.parse.urlsplit(origin)
connection = socket.create_connection((parsed.hostname, parsed.port or (443 if parsed.scheme == "https" else 80)), timeout=15)
if parsed.scheme == "https":
    connection = ssl.create_default_context().wrap_socket(connection, server_hostname=parsed.hostname)
key = base64.b64encode(secrets.token_bytes(16)).decode()
headers = ["GET /api/realtime HTTP/1.1", f"Host: {parsed.netloc}", "Upgrade: websocket", "Connection: Upgrade", "Sec-WebSocket-Version: 13", f"Sec-WebSocket-Key: {key}", f"Origin: {origin}", "Cookie: " + "; ".join(f"{k}={v}" for k, v in cookies.items())]
connection.sendall(("\r\n".join(headers) + "\r\n\r\n").encode())
stream = connection.makefile("rb")
assert stream.readline().split()[1] == b"101", "proxy did not upgrade websocket"
response_headers = {}
while True:
    line = stream.readline()
    if line == b"\r\n":
        break
    name, value = line.decode().split(":", 1)
    response_headers[name.lower()] = value.strip()
expected_accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
assert response_headers["sec-websocket-accept"] == expected_accept, "invalid websocket handshake"

def send_frame(payload, opcode=1):
    data = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
    mask = secrets.token_bytes(4)
    length = len(data)
    prefix = bytes([0x80 | opcode, 0x80 | length]) if length < 126 else bytes([0x80 | opcode, 0x80 | 126]) + struct.pack("!H", length)
    connection.sendall(prefix + mask + bytes(value ^ mask[i % 4] for i, value in enumerate(data)))

def notice():
    while True:
        head = stream.read(2)
        assert len(head) == 2, "websocket disconnected"
        opcode, length = head[0] & 15, head[1] & 127
        if length == 126:
            length = struct.unpack("!H", stream.read(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", stream.read(8))[0]
        assert length <= 4096, "unbounded notification"
        data = stream.read(length)
        if opcode == 9:
            send_frame(data, 10)
            continue
        assert opcode == 1, "unexpected websocket frame"
        return json.loads(data)

assert notice()["type"] == "ready", "socket not ready"
send_frame({"type": "subscribe", "channel_id": channel["id"]})
while notice()["type"] != "subscribed":
    pass
message = mutate("POST", f"/channels/{channel['id']}/messages", {"body": f"Launch: slipgraph://task/{launch['id']}", "mention_ids": [], "thread_id": None, "client_id": str(uuid.uuid4())})
assert message["task_links"][0]["id"] == launch["id"], "task link did not resolve"
for attempt in range(30):
    event = notice()
    if event.get("object_id") == message["id"] and event.get("event_type") == "message.created":
        assert "body" not in event, "message content entered pubsub"
        break
else:
    raise AssertionError("worker did not fan out message")
assert request("GET", f"/channels/{channel['id']}/messages")["items"][0]["id"] == message["id"]
send_frame(struct.pack("!H", 1000), 8)
stream.close()
connection.close()

audit = request("GET", "/audit/verify")
assert audit["valid"] and audit["entries"] >= 6, "invalid audit chain"
# Pass the temporary access credential on stdin, never in process arguments/logs.
verification = subprocess.run(
    ["docker", "compose", "exec", "-T", "api", "sh", "-c",
     "IFS= read -r SLIPGRAPH_SESSION; export SLIPGRAPH_SESSION; exec /app/slipgraph audit verify"],
    input=(cookies["__Host-slipgraph_access"] + "\n").encode(),
    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False,
)
assert verification.returncode == 0, "audit CLI failed"
assert json.loads(verification.stdout)["valid"], "audit CLI rejected chain"
mutate("DELETE", f"/projects/{project['id']}", {"version": project["version"]}, expected=204)
request("GET", f"/tasks/{first['id']}", expected=404)
mutate("POST", "/auth/logout", expected=204)
request("GET", "/auth/session", expected=401)
print("Compose smoke passed: secure sessions, CSRF, CRUD, cycles, worker risk, websocket proxy/fanout, task cards, audit and soft delete.")
