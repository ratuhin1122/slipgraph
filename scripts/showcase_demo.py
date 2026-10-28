"""Deterministic, asserted end-to-end showcase through real API/worker paths."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import uuid
from showcase_client import Client, ORIGIN, ROOT, accounts, assert_mock, load_private, save_private, wait_for

BRIEF = """Launch notes — start 2030-03-04. Need a stable backend API first. Frontend
integration keeps moving; it must wait for the API. Then launch, no surprises.
Please make the order explicit, and let me review everything before it is created."""


def approve(client, proposal_id):
    proposal = client.request("GET", "/proposals/" + proposal_id)
    assert proposal["status"] == "pending", "Demo approval must be an explicit pending human decision"
    client.mutate("POST", f"/proposals/{proposal_id}/approve", {"version": proposal["version"], "payload_hash": proposal["payload_hash"]})
    return wait_for(lambda: client.request("GET", "/proposals/" + proposal_id), lambda value: value["status"] == "executed", "Approved proposal")


def search(client, query, kind="search"):
    request = client.mutate("POST", "/" + kind, {"query": query}, expected=202)
    result = wait_for(lambda: client.request("GET", "/search/" + request["id"]), lambda value: value["status"] == "complete", "Search/Ask")
    assert result["mock"], "Showcase must never use a real answer or embedding provider"
    return result


def run(pause=False):
    manifest = load_private("manifest.json")
    if not manifest:
        raise SystemExit("Run make seed first")
    fixture = accounts()
    a, b = Client(), Client()
    a.login(fixture["accounts"][0])
    a.workspace(manifest["workspace_a"])
    assert_mock(a)
    stages = []

    def stage(name, url, **facts):
        stages.append({"step": name, **facts})
        save_private("live.json", {"stage": name, "url": ORIGIN + url, **facts})
        print("PASS " + name, flush=True)
        if pause:
            print("Capture actual UI at: " + ORIGIN + url, flush=True)
            input("Press Enter to continue (no credentials are printed). ")

    existing_projects = {project["id"] for project in a.request("GET", "/projects")}
    queued = a.mutate("POST", "/plans", {"brief": BRIEF})
    plan_run = wait_for(lambda: a.request("GET", "/agent-runs/" + queued["id"]), lambda value: value["status"] == "completed", "Brief planning")
    assert len(plan_run["result"]["proposal_ids"]) == 1
    assert {p["id"] for p in a.request("GET", "/projects")} == existing_projects, "Unapproved plan wrote business data"
    plan_id = plan_run["result"]["proposal_ids"][0]
    stage("Messy brief produces one pending plan", "/plans?run=" + queued["id"], proposals=1, task_writes=0)
    approve(a, plan_id)
    project = next(p for p in a.request("GET", "/projects") if p["id"] not in existing_projects)
    board = a.request("GET", "/projects/" + project["id"])
    assert len(board["tasks"]) == 3 and len(board["dependencies"]) == 2
    backend = next(task for task in board["tasks"] if task["title"] == "Backend API")
    before = {task["id"]: task for task in board["tasks"]}
    stage("Human approves the project and dependency graph", "/projects/" + project["id"], tasks=3, dependencies=2)
    message = a.mutate("POST", f"/channels/{manifest['channel_a']}/messages", {"body": "backend API will slip 3 days slipgraph://task/" + backend["id"], "client_id": str(uuid.uuid4()), "thread_id": None, "mention_ids": []})
    stage("Chat reports a three-day backend delay", "/chat/" + manifest["channel_a"] + "?message=" + message["id"], delay_days=3)
    impacts = wait_for(lambda: a.request("GET", f"/projects/{project['id']}/impacts"), lambda value: len(value) == 1, "Delay impact")
    scenario = impacts[0]
    result = scenario["result"]
    assert result["launch_flagged"] and result["assessment"]["launch_slip_minutes"] == 4320
    assert len(result["proposal_ids"]) == 3
    unchanged = a.request("GET", "/projects/" + project["id"])
    assert all(task["version"] == before[task["id"]]["version"] and task["due"] == before[task["id"]]["due"] for task in unchanged["tasks"]), "Agent changed task dates before approval"
    stage("Deterministic impact; dates remain unchanged", f"/projects/{project['id']}?impact={scenario['id']}", affected_tasks=3, launch_slip_days=3, pending_proposals=3)
    stage("Review before and after; approval is separate", "/proposals#" + result["proposal_ids"][0], pending_proposals=3)
    audit_before = a.request("GET", "/audit/verify")
    for proposal_id in result["proposal_ids"]:
        approve(a, proposal_id)
    after = a.request("GET", "/projects/" + project["id"])
    assert all(task["version"] == before[task["id"]]["version"] + 1 for task in after["tasks"])
    stage("Approved changes execute exactly once", "/proposals#" + result["proposal_ids"][0], executed_proposals=3)
    audit_after = a.request("GET", "/audit/verify")
    assert audit_after["valid"] and audit_after["entries"] > audit_before["entries"]
    # Verify the actual chain with the authenticated CLI; token travels on stdin only.
    cli = subprocess.run(["docker", "compose", "exec", "-T", "api", "sh", "-c", "IFS= read -r SLIPGRAPH_SESSION; export SLIPGRAPH_SESSION; exec /app/slipgraph audit verify"], input=(a.cookies["__Host-slipgraph_access"] + "\n").encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45, check=False)
    assert cli.returncode == 0 and json.loads(cli.stdout)["valid"], "Audit CLI verification failed"
    stage("Append-only audit chain verifies", "/projects/" + project["id"], audit_valid=True, audit_entries=audit_after["entries"])
    # Eventually-consistent index: prove the changed task is retrievable before Ask.
    wait_for(lambda: search(a, "Backend API"), lambda value: backend["id"] in {item["object_id"] for item in value["items"]}, "Task indexing")
    answer = search(a, "Backend API launch", "ask")
    assert answer["claims"], "No cited fixture answer"
    allowed = {item["object_id"] for item in answer["items"]}
    assert all(set(claim["object_ids"]) <= allowed for claim in answer["claims"])
    stage("Ask returns server-validated workspace citations", "/projects/" + project["id"], answer_claims=len(answer["claims"]), citations=sum(len(c["object_ids"]) for c in answer["claims"]))
    b.login(fixture["accounts"][1])
    b.workspace(manifest["workspace_b"])
    denied = ["/projects/" + project["id"], "/tasks/" + backend["id"], "/channels/" + manifest["channel_a"], "/messages/" + message["id"], "/proposals/" + result["proposal_ids"][0], "/meetings/" + manifest["meeting_a"] + "/media"]
    for path in denied:
        b.request("GET", path, expected=404)
    # Target a phrase absent from B, rather than a generic term that its own vector search can match.
    b_visible = set()
    for own_project in b.request("GET", "/projects"):
        b_visible.update(task["id"] for task in b.request("GET", "/projects/" + own_project["id"])["tasks"])
    for own_channel in b.request("GET", "/channels"):
        b_visible.update(message["id"] for message in b.request("GET", f"/channels/{own_channel['id']}/messages")["items"])
    for own_meeting in b.request("GET", "/meetings"):
        b_visible.update(segment["id"] for segment in b.request("GET", f"/meetings/{own_meeting['id']}/transcript")["items"])
    b_search = search(b, "Backend API launch")
    assert {item["object_id"] for item in b_search["items"]} <= b_visible
    b_answer = search(b, "Backend API launch", "ask")
    b_ids = {item["object_id"] for item in b_answer["items"]}
    assert b_ids <= b_visible and not (b_ids & allowed)
    assert all(set(claim["object_ids"]) <= b_ids and not (set(claim["object_ids"]) & allowed) for claim in b_answer["claims"])
    assert manifest["workspace_a"] not in {w["id"] for w in b.request("GET", "/auth/workspaces")}
    stage("Separate identity cannot access the launch workspace", "/projects/" + project["id"], cross_tenant_denials=len(denied), foreign_search_hits=0, foreign_ask_citations=0)
    report = {"recorded_at_utc": datetime.now(timezone.utc).isoformat(), "mock": True, "seed_tasks": 60, "seed_dependencies": 63, "stages": stages, "date_changes": [{"task": task["title"], "before_due": before[task["id"]]["due"], "after_due": task["due"]} for task in after["tasks"]], "audit": {"valid": audit_after["valid"], "entries": audit_after["entries"]}, "limitations": ["Three-task fixed plan and fixture answers, not real model reasoning", "Audit stage verifies the real append-only chain; raw audit-table SELECT remains forbidden", "Credentials and live object IDs stay in ignored .showcase files"]}
    (ROOT / "docs" / "SHOWCASE_RESULT.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    manifest["demo_project"] = project["id"]
    manifest["demo_scenario"] = scenario["id"]
    manifest["demo_plan_run"] = queued["id"]
    manifest["demo_proposal"] = result["proposal_ids"][0]
    save_private("manifest.json", manifest)
    a.mutate("POST", "/auth/logout", expected=204)
    b.mutate("POST", "/auth/logout", expected=204)
    print("Showcase complete. Sanitized evidence: docs/SHOWCASE_RESULT.json", flush=True)
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pause", action="store_true", help="Pause at verified stages to capture actual product UI")
    run(parser.parse_args().pause)
