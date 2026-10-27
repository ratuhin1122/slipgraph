"""Exercise the production worker with the deterministic mock, never a live API key."""
import secrets
import time
import uuid
from smoke_client import request, mutate

request("GET", "/healthz")
mutate("POST", "/auth/signup", {"email": f"signature-{secrets.token_hex(8)}@example.com", "password": secrets.token_urlsafe(32), "display_name": "Signature demo", "workspace_name": "Signature demo"})
assert request("GET", "/agents/settings")["mode"] == "mock", "signature smoke requires LLM_PROVIDER=mock"
def wait_for(read, predicate):
    for _ in range(80):
        result = read()
        if predicate(result):
            return result
        time.sleep(0.5)
    raise AssertionError("worker did not complete signature workflow")
def approve(proposal):
    mutate("POST", f"/proposals/{proposal['id']}/approve", {"version": proposal["version"], "payload_hash": proposal["payload_hash"]})
queued = mutate("POST", "/plans", {"brief": "Start 2030-01-01. Build backend API, integrate frontend, then launch. Make this a coherent plan."})
run = wait_for(lambda: request("GET", f"/agent-runs/{queued['id']}"), lambda r: r["status"] == "completed")
assert len(run["result"]["proposal_ids"]) == 1
assert request("GET", "/projects") == [], "unapproved plan created a project"
proposal = request("GET", "/proposals/" + run["result"]["proposal_ids"][0])
approve(proposal)
wait_for(lambda: request("GET", "/proposals/" + proposal["id"]), lambda p: p["status"] == "executed")
project = request("GET", "/projects")[0]
board = request("GET", "/projects/" + project["id"])
assert len(board["tasks"]) == 3 and len(board["dependencies"]) == 2
backend = next(t for t in board["tasks"] if t["title"] == "Backend API")
channel = mutate("POST", "/channels", {"name": "launch-planning"})
def post(body):
    return mutate("POST", f"/channels/{channel['id']}/messages", {"body": body, "client_id": str(uuid.uuid4()), "thread_id": None, "mention_ids": []})
post("backend API will slip 3 days")
scenarios = wait_for(lambda: request("GET", f"/projects/{project['id']}/impacts"), lambda r: len(r) == 1)
result = scenarios[0]["result"]
assert result["launch_flagged"] and result["assessment"]["launch_slip_minutes"] == 4320
assert len(result["proposal_ids"]) == 3
assert request("GET", f"/tasks/{backend['id']}")["due"] == backend["due"], "agent changed dates before approval"
messages = request("GET", f"/channels/{channel['id']}/messages")["items"]
assert any(m.get("system_run_id") == scenarios[0]["id"] for m in messages), "missing system impact card"
for proposal_id in result["proposal_ids"]:
    approve(request("GET", "/proposals/" + proposal_id))
for proposal_id in result["proposal_ids"]:
    wait_for(lambda: request("GET", "/proposals/" + proposal_id), lambda p: p["status"] == "executed")
assert request("GET", f"/tasks/{backend['id']}")["due"].startswith("2030-01-05")
post("maybe backend API will slip 3 days")
notices = wait_for(lambda: request("GET", f"/channels/{channel['id']}/messages")["items"], lambda rows: len([m for m in rows if m.get("system_run_id")]) == 2)
latest = next(m for m in reversed(notices) if m.get("system_run_id") and m["system_run_id"] != scenarios[0]["id"])
low = request("GET", "/agent-runs/" + latest["system_run_id"])
assert low["status"] == "needs_confirmation" and low["result"]["proposal_ids"] == []
assert request("GET", "/audit/verify")["valid"], "invalid audit chain"
mutate("POST", "/auth/logout", expected=204)
print("Signature smoke passed: brief -> single plan approval -> project/tasks/dependencies -> chat delay -> deterministic impact -> system card -> approved reschedule, low-confidence confirmation, and audit.")
