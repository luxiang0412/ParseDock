#!/usr/bin/env python3
"""Unit tests for benchmark/scripts/gate.py (PR-11)."""
from __future__ import annotations

import csv
import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def load_gate():
    spec = importlib.util.spec_from_file_location("gate", ROOT / "benchmark/scripts/gate.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class GateTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        (self.dir / "summary").mkdir()
        self.gate = load_gate()
        self.gate.SUMMARY = self.dir / "summary"
        self.gate.BASELINE = self.dir / "baseline.json"

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, quality_rows, perf_rows, engines):
        with (self.dir / "summary" / "benchmarks-quality.csv").open("w", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=["engine", "file", "cer", "table_cell_accuracy"])
            w.writeheader()
            for r in quality_rows:
                w.writerow(r)
        with (self.dir / "summary" / "benchmarks-perf.csv").open("w", newline="") as fh:
            w = csv.DictWriter(fh, fieldnames=["engine", "file", "p50_ms", "p95_ms"])
            w.writeheader()
            for r in perf_rows:
                w.writerow(r)
        (self.dir / "summary" / "scores.json").write_text(json.dumps({"engines": engines}))

    def base_case(self):
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "p50_ms": "100", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        sys.argv = ["gate.py", "--update"]
        self.assertEqual(self.gate.main(), 0)

    def run_gate(self, extra=()):
        sys.argv = ["gate.py", *extra]
        return self.gate.main()

    def test_pass_when_unchanged(self):
        self.base_case()
        self.assertEqual(self.run_gate(), 0)

    def test_quality_drop_fails(self):
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "p50_ms": "100", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 8.5}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_latency_regression_fails(self):
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "p50_ms": "100", "p95_ms": "260"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_cer_jump_fails(self):
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.05", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "p50_ms": "100", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_missing_baseline_is_an_error(self):
        self.write([{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
                   [{"engine": "xberg", "file": "a.pdf", "p50_ms": "100", "p95_ms": "200"}],
                   [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 2)


if __name__ == "__main__":
    unittest.main()
