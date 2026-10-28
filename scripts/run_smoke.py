"""Run production smoke flows and audit verification within normal signup budgets.

Exactly four accounts are created. Security/Audit reuse those synthetic identities
in fresh workspaces. Optional SHOWCASE_CI reuses two of them in the ignored,
user-restricted fixture directory; ordinary smoke credentials remain in memory.
"""
from pathlib import Path
import runpy
import os

directory = Path(__file__).resolve().parent
for script in ["smoke.py", "signature_smoke.py", "meetings_smoke.py", "search_smoke.py", "security_smoke.py", "github_smoke.py"]:
    print(f"Running {script}", flush=True)
    runpy.run_path(str(directory / script), run_name="__main__")

if os.environ.get("SHOWCASE_CI") == "1":
    runpy.run_path(str(directory / "showcase_ci.py"), run_name="__main__")
runpy.run_path(str(directory / "audit_ci.py"), run_name="__main__")
