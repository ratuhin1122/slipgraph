"""Fixture invariants and local-only safety checks, no database needed."""
from collections import defaultdict, deque
from datetime import datetime
import os
from pathlib import Path
import subprocess
import sys
import unittest
from showcase_data import dependency_indices, task_specs


class ShowcaseFixtureTests(unittest.TestCase):
    def test_all_workstreams_reach_launch_without_cycles(self):
        tasks, edges = task_specs(None), dependency_indices()
        self.assertEqual(len(tasks), 60)
        self.assertEqual(len({t["title"] for t in tasks}), 60)
        self.assertEqual(len(edges), 63)
        forward, backward = defaultdict(list), defaultdict(list)
        indegree = [0] * 60
        for dependent, blocker in edges:
            self.assertTrue(0 <= dependent < 60 and 0 <= blocker < 60)
            forward[blocker].append(dependent)
            backward[dependent].append(blocker)
            indegree[dependent] += 1
        queue = deque(i for i, degree in enumerate(indegree) if not degree)
        visited = []
        while queue:
            node = queue.popleft()
            visited.append(node)
            for child in forward[node]:
                indegree[child] -= 1
                if not indegree[child]:
                    queue.append(child)
        self.assertEqual(len(visited), 60, "fixture must be a DAG")
        ancestors, queue = set(), deque(backward[59])
        while queue:
            node = queue.popleft()
            if node not in ancestors:
                ancestors.add(node)
                queue.extend(backward[node])
        self.assertEqual(len(ancestors), 59, "the final launch must depend on every workstream")

    def test_dates_and_milestones_are_valid_calendar_inputs(self):
        tasks = task_specs(None)
        self.assertEqual(sum(t["milestone"] for t in tasks), 6)
        for task in tasks:
            self.assertLessEqual(datetime.fromisoformat(task["start"]), datetime.fromisoformat(task["due"]))
            self.assertEqual(task["estimate_minutes"] == 0, task["milestone"])
            self.assertIn(task["status"], ("todo", "in_progress", "blocked"))

    def test_remote_origin_refused_before_network_or_credentials(self):
        for origin in ("https://example.invalid", "http://:private@localhost:5173", "http://localhost:5173/forbidden"):
            result = subprocess.run([sys.executable, "-c", "import showcase_client"], cwd=Path(__file__).parent, env=dict(os.environ, PUBLIC_ORIGIN=origin), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("only target a loopback origin", result.stderr)

    def test_private_showcase_material_is_ignored(self):
        root = Path(__file__).resolve().parent.parent
        result = subprocess.run(["git", "check-ignore", ".showcase/credentials.json", ".showcase/frames/frame001.png"], cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(len(result.stdout.splitlines()), 2)


if __name__ == "__main__":
    unittest.main()
