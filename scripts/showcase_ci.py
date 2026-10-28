"""Reuse only in-process synthetic smoke identities, respecting real signup budgets."""
from smoke_client import fixture_accounts
from showcase_client import Client, ORIGIN, load_private, save_private
from showcase_seed import seed
from showcase_demo import run

assert len(fixture_accounts) >= 2, "Run through SHOWCASE_CI=1 scripts/run_smoke.py"
assert load_private("credentials.json") is None, "CI adoption refuses to overwrite existing private fixtures"
records = []
manifest = {"version": 1, "origin": ORIGIN}
for index, (name, workspace) in enumerate([("Maya Chen", "Meridian Launch"), ("Noah Patel", "Northstar Research")]):
    account = {**fixture_accounts[index], "name": name, "workspace": workspace}
    client = Client()
    client.login(account)
    session = client.mutate("POST", "/auth/workspaces", {"name": workspace})
    manifest["workspace_a" if index == 0 else "workspace_b"] = session["workspace_id"]
    client.mutate("POST", "/auth/logout", expected=204)
    records.append(account)
save_private("credentials.json", {"origin": ORIGIN, "accounts": records})
save_private("manifest.json", manifest)
seed()
seed()  # Repeat must preserve the same project/task/dependency counts.
run()
