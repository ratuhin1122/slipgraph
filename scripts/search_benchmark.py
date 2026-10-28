"""Run the isolated 100k-object Docker benchmark and record honest measured results."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys
sys.stdout.reconfigure(encoding="utf-8")

root = Path(__file__).resolve().parent.parent
env = dict(os.environ, SEARCH_BENCH="1", GOTOOLCHAIN=os.environ.get("GOTOOLCHAIN", "go1.26.10"))
run = subprocess.run(["go", "test", "-count=1", "-v", "-tags=integration", "-run", "^TestSearchBenchmark$", "-timeout=10m", "./internal/auth"], cwd=root, env=env, text=True, encoding="utf-8", errors="replace", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
print(run.stdout)
if run.returncode:
    raise SystemExit(run.returncode)
match = re.search(r"SEARCH_BENCH_RESULT=(\{[^\n]+\})", run.stdout)
if not match:
    raise SystemExit("Benchmark produced no measurement")
data = json.loads(match.group(1))
(root / "docs" / "SEARCH_BENCHMARK.json").write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
section = f"""<!-- search-benchmark:start -->
### Search benchmark

100,000 synthetic task objects, 384-dimensional mock vectors in 100 buckets,
one workspace, 10 warmups and 100 serial warm queries against isolated Docker Postgres.
Measured p50 **{data['p50_ms']:.2f} ms**, p95 **{data['p95_ms']:.2f} ms**.
Includes hybrid SQL, tenant transaction and live session/policy checks. Excludes
indexing, worker queue/poll delay, remote embeddings and answer generation.
This synthetic warm-cache result is not a production latency guarantee.
Run `python scripts/search_benchmark.py` to reproduce. Details: [measurement](docs/SEARCH_BENCHMARK.json).
<!-- search-benchmark:end -->"""
readme = root / "README.md"
text = readme.read_text(encoding="utf-8")
if "<!-- search-benchmark:start -->" in text:
    text = re.sub(r"<!-- search-benchmark:start -->.*?<!-- search-benchmark:end -->", lambda _: section, text, flags=re.S)
else:
    text += "\n" + section + "\n"
readme.write_text(text, encoding="utf-8")
