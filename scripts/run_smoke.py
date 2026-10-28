"""Run production smoke flows and audit verification within normal signup budgets.

Exactly four accounts are created. Security/Audit reuse those synthetic identities
in fresh workspaces; credentials exist only in this Python process's memory.
"""
from pathlib import Path
import runpy

directory = Path(__file__).resolve().parent
for script in ["smoke.py", "signature_smoke.py", "meetings_smoke.py", "search_smoke.py", "security_smoke.py", "github_smoke.py", "audit_ci.py"]:
    print(f"Running {script}", flush=True)
    runpy.run_path(str(directory / script), run_name="__main__")
