#!/usr/bin/env python3
"""Unit tests for the CI merge-gate aggregation (PR00-08/PR-01 requirement 3).

The first implementation used a substring match and reported success while the
contract job had failed; these tests pin the correct behaviour.

    python3 tests/unit/test_ci_summary.py
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import ci_summary  # noqa: E402


def results(**overrides: str) -> dict[str, str]:
    base = {name: "success" for name in ci_summary.REQUIRED}
    base.update(overrides)
    return base


class TestEvaluate(unittest.TestCase):
    def test_all_success(self):
        ok, problems = ci_summary.evaluate(results())
        self.assertTrue(ok)
        self.assertEqual(problems, [])

    def test_any_failure_blocks(self):
        ok, problems = ci_summary.evaluate(results(contract="failure"))
        self.assertFalse(ok)
        self.assertIn("contract: failure", problems)

    def test_the_false_green_regression(self):
        # The exact case that slipped through: a failure whose name/value also
        # contains the substring "=success" elsewhere in the line.
        line = "static=success unit=success contract=failure lifecycle=success image=success"
        parsed = ci_summary.parse([line])
        ok, problems = ci_summary.evaluate(parsed)
        self.assertFalse(ok, "a failing contract job must fail the summary")
        self.assertIn("contract: failure", problems)

    def test_cancelled_and_skipped_block(self):
        for value in ("cancelled", "skipped", "neutral", ""):
            ok, problems = ci_summary.evaluate(results(lifecycle=value))
            self.assertFalse(ok, f"{value!r} must not count as success")
            self.assertIn("lifecycle", problems[0])

    def test_docs_only_accepts_skipped_but_not_failure(self):
        skipped = {name: "skipped" for name in ci_summary.REQUIRED}
        self.assertTrue(ci_summary.evaluate(skipped, docs_only=True)[0])
        self.assertFalse(ci_summary.evaluate(skipped)[0], "skipped must block without --docs-only")
        for value in ("failure", "cancelled", ""):
            ok, _ = ci_summary.evaluate(results(lifecycle=value), docs_only=True)
            self.assertFalse(ok, f"{value!r} must block even for a docs-only change")

    def test_summary_job_is_not_self_referential(self):
        # The aggregator cannot report its own result; it must not require itself.
        self.assertNotIn("ci-summary", ci_summary.REQUIRED)

    def test_missing_job_blocks(self):
        r = results()
        del r["image"]
        ok, problems = ci_summary.evaluate(r)
        self.assertFalse(ok)
        self.assertTrue(any(p.startswith("image: missing") for p in problems))

    def test_parse_accepts_multiple_arguments_and_lines(self):
        parsed = ci_summary.parse(["static=success", "unit=success\ncontract=success"])
        self.assertEqual(parsed["static"], "success")
        self.assertEqual(parsed["contract"], "success")

    def test_parse_is_case_insensitive_for_results(self):
        parsed = ci_summary.parse(["static=SUCCESS"])
        ok, _ = ci_summary.evaluate(results(**parsed))
        self.assertTrue(ok)


class TestRender(unittest.TestCase):
    def test_table_contains_every_required_job(self):
        text = ci_summary.render(results(), [])
        for name in ci_summary.REQUIRED:
            self.assertIn(name, text)
        self.assertIn("All required jobs succeeded", text)

    def test_blocking_section_lists_problems(self):
        r = results(contract="failure")
        ok, problems = ci_summary.evaluate(r)
        text = ci_summary.render(r, problems)
        self.assertIn("**Blocking:**", text)
        self.assertIn("contract: failure", text)


if __name__ == "__main__":
    unittest.main(verbosity=2)
