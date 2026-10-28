"""Local-only showcase client. Normal sessions, CSRF, permissions and rate limits."""
import csv
import http.cookies
import io
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
PRIVATE = ROOT / ".showcase"
ORIGIN = os.environ.get("PUBLIC_ORIGIN", "http://localhost:5173").rstrip("/")
parsed = urllib.parse.urlparse(ORIGIN)
if parsed.scheme not in ("http", "https") or parsed.hostname not in ("localhost", "127.0.0.1", "::1") or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment or parsed.path:
    raise SystemExit("Showcase commands only target a loopback origin, without credentials or a path.")


def private_directory():
    PRIVATE.mkdir(exist_ok=True, mode=0o700)
    if not PRIVATE.resolve().is_relative_to(ROOT.resolve()) or PRIVATE.is_symlink():
        raise SystemExit("Showcase directory must remain inside the repository.")
    if os.name == "nt":
        # Restrict this newly created fixture directory, never the repository ACL.
        row = subprocess.run(["whoami", "/user", "/fo", "csv", "/nh"], check=True, stdout=subprocess.PIPE, text=True).stdout
        sid = next(csv.reader(io.StringIO(row)))[1]
        subprocess.run(["icacls", str(PRIVATE), "/inheritance:r", "/grant:r", f"*{sid}:(OI)(CI)F", "*S-1-5-18:(OI)(CI)F"], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    else:
        PRIVATE.chmod(0o700)


def save_private(name, value):
    private_directory()
    path = PRIVATE / name
    if path.is_symlink() or not path.resolve().is_relative_to(PRIVATE.resolve()):
        raise RuntimeError("Invalid showcase file")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")


def load_private(name, default=None):
    path = PRIVATE / name
    if PRIVATE.is_symlink() or path.is_symlink() or not path.resolve().is_relative_to(PRIVATE.resolve()):
        raise RuntimeError("Invalid showcase file")
    return json.loads(path.read_text(encoding="utf-8")) if path.exists() else default


class APIError(RuntimeError):
    def __init__(self, method, expected, actual):
        self.status = actual
        super().__init__(f"{method} endpoint failed: expected {expected}, received {actual}")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self):
        self.cookies = {}
        self.last = 0.0
        self.authenticated = False
        self.auth_at = 0.0
        self.refreshing = False
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def request(self, method, path, body=None, expected=200, raw=False, content_type=None, csrf=None):
        if method == "GET" and self.authenticated and not self.refreshing and time.monotonic() - self.auth_at > 480:
            self.refreshing = True
            try:
                self.mutate("POST", "/auth/refresh")
            finally:
                self.refreshing = False
        time.sleep(max(0, 0.36 - (time.monotonic() - self.last)))
        self.last = time.monotonic()
        headers = {"Origin": ORIGIN}
        if self.cookies:
            headers["Cookie"] = "; ".join(k + "=" + v for k, v in self.cookies.items())
        if csrf:
            headers["X-CSRF-Token"] = csrf
        data = None
        if body is not None:
            data = body if isinstance(body, bytes) else json.dumps(body).encode()
            headers["Content-Type"] = content_type or "application/json"
        transport = ORIGIN
        if parsed.scheme == "http" and parsed.hostname == "localhost":
            transport = "http://127.0.0.1" + (":" + str(parsed.port) if parsed.port else "")
            headers["Host"] = parsed.netloc
        request = urllib.request.Request(transport + "/api" + path, data=data, headers=headers, method=method)
        try:
            response = self.opener.open(request, timeout=45)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            for value in response.headers.get_all("Set-Cookie", []):
                for key, cookie in http.cookies.SimpleCookie(value).items():
                    assert cookie["secure"] and cookie["httponly"] and cookie["samesite"] == "Lax", "unsafe cookie"
                    self.cookies[key] = cookie.value
            if response.status != expected:
                # Never interpolate cookies, bodies, tokens or response payloads.
                raise APIError(method, expected, response.status)
            if method == "POST" and path in ("/auth/signup", "/auth/login", "/auth/refresh", "/auth/workspace-switch"):
                self.authenticated = True
                self.auth_at = time.monotonic()
            if path == "/auth/logout":
                self.authenticated = False
            value = response.read()
            return value if raw else json.loads(value) if value else None

    def mutate(self, method, path, body=None, expected=200, content_type=None):
        token = self.request("GET", "/auth/csrf")["csrf_token"]
        return self.request(method, path, body, expected, content_type=content_type, csrf=token)

    def login(self, account):
        self.cookies.clear()
        return self.mutate("POST", "/auth/login", {"email": account["email"], "password": account["password"]})

    def workspace(self, workspace_id):
        session = self.request("GET", "/auth/session")
        if session["workspace_id"] == workspace_id:
            return session
        directory = self.request("GET", "/auth/workspaces")
        own = next((w for w in directory if w["id"] == workspace_id), None)
        if not own:
            raise RuntimeError("Fixture workspace is not in this user's verified directory")
        return self.mutate("POST", "/auth/workspace-switch", {"ticket": own["switch_token"]})


def accounts():
    value = load_private("credentials.json")
    if value:
        if value["origin"] != ORIGIN:
            raise RuntimeError("Showcase credentials belong to another loopback origin")
        return value
    suffix = secrets.token_hex(6)
    value = {"origin": ORIGIN, "accounts": [
        {"email": f"maya-{suffix}@showcase.example", "password": secrets.token_urlsafe(32), "name": "Maya Chen", "workspace": "Meridian Launch"},
        {"email": f"noah-{suffix}@showcase.example", "password": secrets.token_urlsafe(32), "name": "Noah Patel", "workspace": "Northstar Research"},
    ]}
    save_private("credentials.json", value)
    return value


def wait_for(read, predicate, label, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = read()
        if predicate(value):
            return value
        if isinstance(value, dict) and value.get("status") in ("failed", "expired", "rejected"):
            raise RuntimeError(label + " failed")
        time.sleep(1)
    raise RuntimeError(label + " timed out; inspect worker health and mock settings")


def assert_mock(client):
    assert client.request("GET", "/agents/settings")["mode"] == "mock", "Showcase requires LLM_PROVIDER=mock"
    assert client.request("GET", "/meetings/settings")["mock"], "Showcase requires mock transcription"
