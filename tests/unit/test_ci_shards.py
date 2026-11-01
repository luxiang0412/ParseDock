"""The CI lifecycle shards must run exactly the acceptance cases of the full lifecycle profile.

scripts/ci-local.sh splits `lifecycle` into shards that CI runs on separate runners.
A T-case added to tests/integration/acceptance.py but not to a shard would silently stop
running in CI; a case listed twice would waste a runner. Both fail here.
"""
from __future__ import annotations

import ast
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def lifecycle_cases() -> list[str]:
    tree = ast.parse((ROOT / "tests/integration/acceptance.py").read_text())
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
                isinstance(t, ast.Name) and t.id == "LIFECYCLE_CASES" for t in node.targets):
            return [ast.literal_eval(e) for e in node.value.elts]
    raise AssertionError("LIFECYCLE_CASES not found")


def shard_cases() -> list[list[str]]:
    text = (ROOT / "scripts/ci-local.sh").read_text()
    m = re.search(r"LIFECYCLE_SHARD_CASES=\((.*?)\n\)", text, re.S)
    assert m, "LIFECYCLE_SHARD_CASES not found in scripts/ci-local.sh"
    entries = re.findall(r'"([^"]*)"', m.group(1))
    return [[c for c in e.split(",") if c] for e in entries]


class TestShards(unittest.TestCase):
    def test_shards_cover_every_case_once(self):
        flat = [c for shard in shard_cases() for c in shard]
        self.assertEqual(sorted(flat), sorted(lifecycle_cases()))
        self.assertEqual(len(flat), len(set(flat)), f"case listed twice: {flat}")

    def test_kill_stack_cases_share_a_shard(self):
        # T09 and T17 share the `kill` stack (CASE_STACK); T17 tolerates running after T09.
        shards = [s for s in shard_cases() if "T09" in s or "T17" in s]
        self.assertEqual(len(shards), 1, shards)

    def test_workflow_runs_every_shard(self):
        wf = (ROOT / ".github/workflows/ci.yml").read_text()
        m = re.search(r"shard:\s*\[([^\]]*)\]", wf)
        self.assertTrue(m, "ci.yml lifecycle matrix not found")
        listed = sorted(int(x) for x in m.group(1).split(","))
        self.assertEqual(listed, list(range(1, len(shard_cases()))))


if __name__ == "__main__":
    unittest.main(verbosity=2)
