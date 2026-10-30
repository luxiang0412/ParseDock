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
            # Mirror the real summarize.py schema: the median column is `median_ms`.
            w = csv.DictWriter(fh, fieldnames=["engine", "file", "median_ms", "p95_ms"])
            w.writeheader()
            for r in perf_rows:
                w.writerow(r)
        (self.dir / "summary" / "scores.json").write_text(json.dumps({"engines": engines}))

    def base_case(self):
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "100", "p95_ms": "200"}],
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
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "100", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 8.5}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_latency_regression_fails(self):
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "100", "p95_ms": "400"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_cer_jump_fails(self):
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.05", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "100", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_small_absolute_latency_noise_is_ignored(self):
        """6.9 → 9.0 ms is 30 % but only 2 ms: millisecond noise, not a regression."""
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "7", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 0)

    def test_median_regression_fails(self):
        """median_ms is the column summarize.py actually writes."""
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "150", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_missing_measurement_fails(self):
        """A baseline-covered sample that disappears must not silently pass."""
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "b.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "b.pdf", "median_ms": "100", "p95_ms": "200"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)
        self.assertEqual(self.run_gate(["--allow-missing"]), 0)

    def test_zero_baseline_latency_regression_fails(self):
        """native CSV baselines round to 0 ms; a jump to seconds must still fail."""
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "0", "p95_ms": "0"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 0)  # baseline now records zeros
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [{"engine": "xberg", "file": "a.pdf", "median_ms": "1000", "p95_ms": "1000"}],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)

    def test_missing_metric_fails(self):
        """A sample present with CER but no latency must not skip the latency check."""
        self.base_case()
        self.write(
            [{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
            [],
            [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 1)
        self.assertEqual(self.run_gate(["--allow-missing"]), 0)

    def test_missing_baseline_is_an_error(self):
        self.write([{"engine": "xberg", "file": "a.pdf", "cer": "0.01", "table_cell_accuracy": "0.9"}],
                   [{"engine": "xberg", "file": "a.pdf", "median_ms": "100", "p95_ms": "200"}],
                   [{"engine": "xberg", "dimensions": {"quality": 9.0}, "quality_sub": {"structure": 9.0}}])
        self.assertEqual(self.run_gate(), 2)


if __name__ == "__main__":
    unittest.main()
