"""Verify a nonempty authenticated chain after the CI test/smoke runs.

The ephemeral session is passed to slipgraph audit verify through stdin, never
through command arguments, the CI environment file, or console output.
"""
import json
import subprocess
from smoke_client import mutate, cookies, fixture_identity

fixture_identity("Audit verification", 0)
mutate("POST", "/projects", {"name": "Audited change", "description": "", "start": "2030-01-01T00:00:00Z", "launch_due": None})
result = subprocess.run(
    ["docker", "compose", "exec", "-T", "api", "sh", "-c", "IFS= read -r SLIPGRAPH_SESSION; export SLIPGRAPH_SESSION; exec /app/slipgraph audit verify"],
    input=(cookies["__Host-slipgraph_access"] + "\n").encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False,
)
assert result.returncode == 0, "slipgraph audit verify failed"
verified = json.loads(result.stdout)
assert verified["valid"] and verified["entries"] >= 2, "invalid or empty audit chain"
mutate("POST", "/auth/logout", expected=204)
print("slipgraph audit verify passed with an authenticated admin and nonempty chain.")
