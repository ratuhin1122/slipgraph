"""Check the deployed proxy's nonce CSP, headers and private recording boundary."""
import io
import re
import secrets
import urllib.request
import wave
from smoke_client import origin, request, mutate, fixture_identity

nonces = []
for _ in range(2):
    req = urllib.request.Request(origin + "/", headers={"X-CSP-Nonce": "attacker-chosen", "X-Request-ID": "attacker-chosen", "If-Modified-Since": "Wed, 28 Oct 2099 00:00:00 GMT"})
    with urllib.request.urlopen(req, timeout=15) as response:
        html = response.read().decode()
        policies = response.headers.get_all("Content-Security-Policy", [])
        policy = "; ".join(policies)
        match = re.search(r"'nonce-([A-Za-z0-9+/]{43})'", policy)
        assert match and "'strict-dynamic'" in policy and "'unsafe-inline'" not in policy and "'unsafe-eval'" not in policy, "weak document CSP"
        nonce = match.group(1)
        assert "__SLIPGRAPH_NONCE__" not in html and "attacker-chosen" not in html
        scripts = re.findall(r"<script\b([^>]*)>", html)
        assert scripts and all(f'nonce="{nonce}"' in attributes for attributes in scripts), "script nonce/header mismatch"
        assert "no-store" in response.headers.get("Cache-Control", "")
        for name in ["Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Cross-Origin-Resource-Policy", "Cross-Origin-Opener-Policy"]:
            assert response.headers.get(name), f"missing {name}"
        nonces.append(nonce)
assert nonces[0] != nonces[1], "nonce was reused"

fixture_identity("Security A", 0)
project = mutate("POST", "/projects", {"name": "Private recording", "description": "", "start": "2030-01-01T00:00:00Z", "launch_due": None})
stream = io.BytesIO()
with wave.open(stream, "wb") as audio:
    audio.setnchannels(1)
    audio.setsampwidth(2)
    audio.setframerate(16000)
    audio.writeframes(bytes(32000))
boundary = "security-" + secrets.token_hex(16)
body = b""
for name, value in {"title": "Private recording", "project_id": project["id"]}.items():
    body += f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode()
body += f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="../../evil.html"\r\nContent-Type: audio/wav\r\n\r\n'.encode() + stream.getvalue() + f'\r\n--{boundary}--\r\n'.encode()
csrf = request("GET", "/auth/csrf")["csrf_token"]
meeting = request("POST", "/meetings", body, csrf, content_type="multipart/form-data; boundary=" + boundary)
from smoke_client import cookies  # Session stays only in process memory.
req = urllib.request.Request(origin + "/api/meetings/" + meeting["id"] + "/media", headers={"Cookie": "; ".join(f"{key}={value}" for key, value in cookies.items())})
with urllib.request.urlopen(req, timeout=15) as response:
    assert response.read() == stream.getvalue()
    assert any("sandbox" in value for value in response.headers.get_all("Content-Security-Policy", [])), "proxy discarded private-download CSP"
    assert response.headers.get("Content-Type") == "application/octet-stream"
    assert response.headers.get_all("Cross-Origin-Resource-Policy") == ["same-origin"], "ambiguous CORP header"
    assert response.headers.get_all("Cross-Origin-Opener-Policy") == ["same-origin"], "ambiguous COOP header"
    assert response.headers.get("Content-Disposition", "").startswith("attachment;")
mutate("POST", "/auth/logout", expected=204)
fixture_identity("Security B", 1)
request("GET", "/meetings/" + meeting["id"] + "/media", expected=404)
mutate("POST", "/auth/logout", expected=204)
print("Security smoke passed: fresh nonce CSP, strict headers, preserved media sandbox, inert names and foreign download denial.")
