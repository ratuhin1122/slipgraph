"""Exercise actual media probing, private storage and worker extraction in mock mode."""
import io
import json
import secrets
import time
import wave
from smoke_client import request, mutate

def wait_for(read, predicate):
    for _ in range(100):
        value = read()
        if predicate(value):
            return value
        time.sleep(0.5)
    raise AssertionError("meeting worker did not finish")

request("GET", "/healthz")
mutate("POST", "/auth/signup", {"email": f"meeting-{secrets.token_hex(8)}@example.com", "password": secrets.token_urlsafe(32), "display_name": "Meeting smoke", "workspace_name": "Meeting smoke"})
settings = request("GET", "/meetings/settings")
assert settings["enabled"] and settings["mock"], "meeting smoke requires mock providers"
project = mutate("POST", "/projects", {"name": "Meeting launch", "description": "", "start": "2030-01-01T00:00:00Z", "launch_due": None})
recording = io.BytesIO()
with wave.open(recording, "wb") as wav:
    wav.setnchannels(1)
    wav.setsampwidth(2)
    wav.setframerate(16000)
    wav.writeframes(bytes(64000))
recording = recording.getvalue()

def upload(data, mime="audio/wav", expected=200):
    boundary = "slipgraph-" + secrets.token_hex(16)
    parts = []
    for name, value in {"title": "Planning meeting", "project_id": project["id"]}.items():
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="untrusted.wav"\r\nContent-Type: {mime}\r\n\r\n'.encode() + data + b"\r\n")
    parts.append(f"--{boundary}--\r\n".encode())
    csrf = request("GET", "/auth/csrf")["csrf_token"]
    return request("POST", "/meetings", b"".join(parts), csrf, expected, content_type="multipart/form-data; boundary=" + boundary)

upload(b"<html><script>alert(1)</script>", expected=415)
meeting = upload(recording)
assert request("GET", f"/meetings/{meeting['id']}/media", raw=True) == recording
meeting = wait_for(lambda: request("GET", f"/meetings/{meeting['id']}"), lambda m: m["status"] == "ready" and m["extraction_status"] == "complete")
assert meeting["duration_ms"] == 2000, "ffprobe duration incorrect"
transcript = request("GET", f"/meetings/{meeting['id']}/transcript?q=stable")["items"]
assert len(transcript) == 1 and transcript[0]["speaker"] == "Demo speaker"
assert len(meeting["suggestions"]) == 2
assert request("GET", f"/projects/{project['id']}")["tasks"] == [], "unapproved transcript created tasks"
for suggestion in meeting["suggestions"]:
    evidence = request("GET", f"/meetings/{meeting['id']}/segments/{suggestion['segment_id']}")
    assert evidence["start_ms"] == suggestion["start_ms"]
    proposal = request("GET", "/proposals/" + suggestion["id"])
    assert "segment=" + evidence["id"] in proposal["source_links"][0]["href"]
    mutate("POST", "/proposals/" + proposal["id"] + "/approve", {"version": proposal["version"], "payload_hash": proposal["payload_hash"]})
    wait_for(lambda: request("GET", "/proposals/" + proposal["id"]), lambda p: p["status"] == "executed")
assert len(request("GET", f"/projects/{project['id']}")["tasks"]) == 1
assert request("GET", "/audit/verify")["valid"]
mutate("POST", "/auth/logout", expected=204)
print("Meeting smoke passed: malicious media rejected, private upload/download, actual duration probe, worker transcription/extraction, timestamp evidence, approved task/decision and audit.")
