"""Resumable local seed through authenticated APIs; no SQL/tenant bypass."""
import io
import uuid
import wave
from showcase_client import APIError, Client, ORIGIN, PRIVATE, accounts, assert_mock, load_private, save_private, wait_for
from showcase_data import BASE, dependency_indices, task_specs


def account_session(client, account):
    try:
        return client.login(account)
    except APIError as error:
        if error.status != 401:
            raise
    return client.mutate("POST", "/auth/signup", {"email": account["email"], "password": account["password"], "display_name": account["name"], "workspace_name": account["workspace"]})


def upload_meeting(client, project):
    # Deliberately silent audio: the transcript provider is a labeled mock fixture.
    stream = io.BytesIO()
    with wave.open(stream, "wb") as audio:
        audio.setnchannels(1)
        audio.setsampwidth(2)
        audio.setframerate(16000)
        audio.writeframes(bytes(64000))
    boundary = "showcase-" + uuid.uuid4().hex
    data = b""
    for name, value in {"title": "Launch readiness review (mock transcript)", "project_id": project}.items():
        data += f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode()
    data += f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="showcase.wav"\r\nContent-Type: audio/wav\r\n\r\n'.encode() + stream.getvalue() + f'\r\n--{boundary}--\r\n'.encode()
    return client.mutate("POST", "/meetings", data, content_type="multipart/form-data; boundary=" + boundary)


def seed():
    credentials = accounts()
    manifest = load_private("manifest.json", {"version": 1, "origin": ORIGIN})
    a, b = Client(), Client()
    session = account_session(a, credentials["accounts"][0])
    manifest.setdefault("workspace_a", session["workspace_id"])
    a.workspace(manifest["workspace_a"])
    assert_mock(a)
    projects = a.request("GET", "/projects")
    project = next((p for p in projects if p["name"] == "Meridian • Launch readiness"), None)
    if not project:
        project = a.mutate("POST", "/projects", {"name": "Meridian • Launch readiness", "description": "Fictional 60-task public launch across strategy, platform, experience, quality, go-to-market and release. UTC calendar planning; synthetic demo data.", "start": BASE.isoformat(), "launch_due": "2030-02-08T09:00:00Z"})
    manifest["project_a"] = project["id"]
    save_private("manifest.json", manifest)
    owner = next(m["id"] for m in a.request("GET", "/members") if m["user_id"] == session["user"]["id"])
    board = a.request("GET", "/projects/" + project["id"])
    existing = {t["title"]: t for t in board["tasks"]}
    specs = task_specs(owner)
    for index, spec in enumerate(specs):
        if spec["title"] not in existing:
            existing[spec["title"]] = a.mutate("POST", f"/projects/{project['id']}/tasks", spec)
        if (index + 1) % 10 == 0:
            print(f"Launch tasks ready: {index + 1}/60", flush=True)
    edges = {(e["task_id"], e["blocker_id"]) for e in board["dependencies"]}
    ordered = [existing[s["title"]] for s in specs]
    for dependent, blocker in dependency_indices():
        task = ordered[dependent]
        pair = (task["id"], ordered[blocker]["id"])
        if pair not in edges:
            updated = a.mutate("POST", f"/tasks/{task['id']}/dependencies", {"version": task["version"], "blocker_id": pair[1]})
            task["version"] = updated["version"]
            edges.add(pair)
    channels = {c["name"]: c for c in a.request("GET", "/channels")}
    for name in ("launch-room", "engineering"):
        if name not in channels:
            channels[name] = a.mutate("POST", "/channels", {"name": name})
    manifest["channel_a"] = channels["launch-room"]["id"]
    manifest["task_a"] = ordered[10]["id"]
    if "welcome_message" not in manifest:
        message = a.mutate("POST", f"/channels/{manifest['channel_a']}/messages", {"body": "Launch review: the public launch is planned for 2030-02-08. Track readiness in the timeline, and keep every AI suggestion behind human approval.", "thread_id": None, "client_id": str(uuid.uuid4()), "mention_ids": []})
        manifest["welcome_message"] = message["id"]
    meetings = a.request("GET", "/meetings")
    meeting = next((m for m in meetings if m["project_id"] == project["id"]), None)
    if not meeting:
        meeting = upload_meeting(a, project["id"])
    meeting = wait_for(lambda: a.request("GET", "/meetings/" + meeting["id"]), lambda value: value["status"] == "ready" and value["extraction_status"] == "complete", "Mock meeting")
    transcript = a.request("GET", f"/meetings/{meeting['id']}/transcript")
    assert transcript["items"], "Missing seeded transcript"
    manifest["meeting_a"] = meeting["id"]
    other = account_session(b, credentials["accounts"][1])
    manifest.setdefault("workspace_b", other["workspace_id"])
    b.workspace(manifest["workspace_b"])
    second = next((p for p in b.request("GET", "/projects") if p["name"] == "Northstar • Private pilot"), None)
    if not second:
        second = b.mutate("POST", "/projects", {"name": "Northstar • Private pilot", "description": "Independent fictional customer workspace. Its user has no membership in Meridian.", "start": BASE.isoformat(), "launch_due": None})
    manifest["project_b"] = second["id"]
    if not b.request("GET", "/projects/" + second["id"])["tasks"]:
        task = task_specs(None)[0]
        task.update(title="NORTHSTAR-ONLY customer research", description="Private pilot interviews; must never appear in Meridian search.")
        b.mutate("POST", f"/projects/{second['id']}/tasks", task)
    save_private("manifest.json", manifest)
    final = a.request("GET", "/projects/" + project["id"])
    assert len(final["tasks"]) == 60 and len(final["dependencies"]) == len(dependency_indices())
    b.request("GET", "/projects/" + project["id"], expected=404)
    a.mutate("POST", "/auth/logout", expected=204)
    b.mutate("POST", "/auth/logout", expected=204)
    print(f"Seed ready: 60 tasks, {len(dependency_indices())} dependencies, two launch channels, mock meeting/transcript and isolated second workspace.")
    print("Private login credentials: " + str(PRIVATE / "credentials.json"))
    print("Open " + ORIGIN + "/projects — no passwords or session tokens were printed.")
    return manifest


if __name__ == "__main__":
    seed()
