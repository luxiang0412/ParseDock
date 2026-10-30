#!/usr/bin/env python3
"""Fixture integrity: every manifest entry must match the file on disk (PR-11).

A benchmark whose corpus silently changed is worse than no benchmark, so the hashes are
part of the test suite rather than a comment.
"""
from __future__ import annotations

import hashlib
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "benchmark" / "fixtures"


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


class FixtureManifestTest(unittest.TestCase):
    def check_manifest(self, manifest: Path) -> int:
        data = json.loads(manifest.read_text())
        entries = data.get("fixtures") or []
        self.assertTrue(entries, f"{manifest} lists no fixtures")
        for e in entries:
            path = FIXTURES / e["path"]
            self.assertTrue(path.exists(), f"missing {e['path']}")
            self.assertEqual(path.stat().st_size, e["bytes"], f"size drift: {e['path']}")
            self.assertEqual(sha256(path), e["sha256"], f"hash drift: {e['path']}")
            if e.get("gold"):
                gold = FIXTURES / e["gold"]
                self.assertTrue(gold.exists(), f"missing gold {e['gold']}")
        return len(entries)

    def test_generated_manifest(self):
        n = self.check_manifest(FIXTURES / "manifest.json")
        self.assertGreaterEqual(n, 20)

    def test_corpus_manifest(self):
        n = self.check_manifest(FIXTURES / "corpus" / "manifest.json")
        self.assertGreaterEqual(n, 100, "corpus should be 100+ documents")

    def test_corpus_covers_every_format(self):
        data = json.loads((FIXTURES / "corpus" / "manifest.json").read_text())
        formats = {e["format"] for e in data["fixtures"]}
        for want in ("pdf", "docx", "xlsx", "csv", "txt"):
            self.assertIn(want, formats)

    def test_no_real_corpus_in_repo(self):
        # The real corporate corpus is local-only; nothing under test_data may be tracked.
        tracked = ROOT / "test_data"
        if tracked.exists():
            self.assertFalse((ROOT / ".git" / "index").stat().st_size == 0)
            import subprocess
            out = subprocess.run(["git", "-C", str(ROOT), "ls-files", "test_data"],
                                 capture_output=True, text=True, check=True).stdout.strip()
            self.assertEqual(out, "", "test_data must never be committed")


if __name__ == "__main__":
    unittest.main()
