"""Authenticated local smoke client; credentials stay in memory."""
import http.cookies
import json
import os
import urllib.error
import urllib.request

origin = os.environ.get("PUBLIC_ORIGIN", "http://localhost:5173").rstrip("/")
cookies: dict[str, str] = {}


def request(method: str, path: str, body=None, csrf=None, expected=200, raw=False, content_type=None):
    headers = {"Origin": origin}
    if cookies:
        headers["Cookie"] = "; ".join(f"{key}={value}" for key, value in cookies.items())
    if csrf:
        headers["X-CSRF-Token"] = csrf
    data = None
    if body is not None:
        headers["Content-Type"] = content_type or "application/json"
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
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
        return payload if raw else json.loads(payload) if payload else None


def mutate(method, path, body=None, expected=200):
    csrf = request("GET", "/auth/csrf")["csrf_token"]
    return request(method, path, body, csrf, expected)


