"""Fictional launch fixture; dates/content stable, IDs are minted by the API."""
from datetime import datetime, timedelta, timezone

STREAMS = [
 ("Strategy", ["Define pilot success metrics", "Interview five design partners", "Map launch personas", "Approve launch scope", "Write product positioning", "Review pricing hypotheses", "Document data retention policy", "Complete accessibility plan", "Publish launch narrative", "Scope approved"]),
 ("Platform", ["Publish stable API contract", "Implement workspace authorization", "Complete account lifecycle", "Harden session rotation", "Add task version checks", "Ship dependency graph loader", "Validate critical path engine", "Complete event outbox", "Run restore rehearsal", "Platform ready"]),
 ("Experience", ["Approve dashboard wireframes", "Build project overview", "Polish task drawer", "Finish Kanban keyboard flow", "Ship dependency timeline", "Render risk explanations", "Build proposals inbox", "Polish transcript navigation", "Verify responsive layouts", "Experience ready"]),
 ("Quality", ["Write launch acceptance matrix", "Exercise tenant isolation", "Test WebSocket reconnects", "Run upload abuse fixtures", "Verify cited answers", "Audit cookie and CSP flags", "Load test the launch graph", "Rehearse provider outage", "Close accessibility findings", "Quality gate passed"]),
 ("Go-to-market", ["Draft launch announcement", "Prepare product screenshots", "Record onboarding walkthrough", "Write quickstart guide", "Build design partner FAQ", "Prepare support playbook", "Review public security summary", "Schedule pilot feedback sessions", "Finalize demo script", "Launch communications ready"]),
 ("Release", ["Freeze release candidate", "Run database migration rehearsal", "Verify rollback procedure", "Review operational dashboards", "Check integration permissions", "Complete release checklist", "Run customer onboarding rehearsal", "Confirm launch owners", "Hold go/no-go review", "Meridian public launch"]),
]
BASE = datetime(2030, 1, 7, 9, tzinfo=timezone.utc)
OFFSETS = [0, 3, 6, 10, 5, 14]


def task_specs(owner):
    output = []
    for stream, (label, names) in enumerate(STREAMS):
        for index, title in enumerate(names):
            start = BASE + timedelta(days=OFFSETS[stream] + index * 2)
            milestone = index == 9
            due = start if milestone else start + timedelta(days=1)
            output.append({"title": title, "description": f"Workstream: {label}. Acceptance: demonstrate {title.lower()} in the launch review, link evidence, and obtain a peer review. Fictional showcase fixture.", "status": "in_progress" if index == 0 else "blocked" if stream == 3 and index == 1 else "todo", "owner_id": owner, "start": start.isoformat().replace("+00:00", "Z"), "due": due.isoformat().replace("+00:00", "Z"), "estimate_minutes": 0 if milestone else [240, 480, 720][index % 3], "milestone": milestone})
    return output


def dependency_indices():
    edges = {(stream * 10 + index, stream * 10 + index - 1) for stream in range(6) for index in range(1, 10)}
    edges.update({(10, 3), (20, 13), (30, 24), (40, 8), (50, 39), (50, 49), (55, 19), (59, 29), (59, 9)})
    return sorted(edges)
