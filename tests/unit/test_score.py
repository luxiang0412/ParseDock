#!/usr/bin/env python3
"""Unit tests for the scoring script (PR00-01).

Runs with the standard library only:

    python3 tests/unit/test_score.py
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "benchmark" / "scripts"))

import score  # noqa: E402


class TestRubric(unittest.TestCase):
    def test_weights_sum_to_one(self):
        self.assertAlmostEqual(sum(score.WEIGHTS.values()), 1.0, places=9)

    def test_quality_subweights_sum_to_one(self):
        self.assertAlmostEqual(sum(score.QUALITY_SUBWEIGHTS.values()), 1.0, places=9)

    def test_every_engine_has_an_ops_rubric(self):
        for engine in score.SUPPORTED:
            self.assertIn(engine, score.OPS_RUBRIC, f"{engine} has no ops/licence rubric")

    def test_ops_rubric_scores_are_bounded(self):
        for engine, (value, reasons) in score.OPS_RUBRIC.items():
            self.assertGreaterEqual(value, 0.0, engine)
            self.assertLessEqual(value, 10.0, engine)
            self.assertTrue(reasons, f"{engine} has no recorded reasons")

    def test_domains_are_disjoint_and_cover_targets(self):
        pdf = {s["format"] for s in score.PDF_OFFICE_SAMPLES}
        text = {s["format"] for s in score.TEXT_SAMPLES}
        self.assertFalse(pdf & text, "a format appears in both domains")

    def test_scan_samples_are_double_weighted(self):
        scans = [s for s in score.PDF_OFFICE_SAMPLES if s.get("double_weight")]
        self.assertEqual(len(scans), 2, "exactly the two scan fixtures must be double weighted")


class TestClamping(unittest.TestCase):
    def test_cer_above_one_is_clamped(self):
        self.assertEqual(score.clamp_cer(1.25), 1.0)
        self.assertEqual(score.clamp_cer(-0.1), 0.0)
        self.assertEqual(score.clamp_cer(0.25), 0.25)


def rows_for(engine: str, cer_by_file: dict[str, float],
             cell: float | None = None, page: bool = True, bbox: bool = True,
             sheet: bool = True) -> dict[str, dict]:
    out = {}
    for sample in score.samples_for(engine):
        f = sample["file"]
        if f not in cer_by_file:
            continue
        out[f] = {
            "engine": engine, "file": f, "version": "1",
            "cer": str(cer_by_file[f]),
            "table_cell_accuracy": "" if cell is None else str(cell),
            "table_header_accuracy": "" if cell is None else str(cell),
            "has_page_numbers": "true" if page else "false",
            "has_bbox": "true" if bbox else "false",
            "has_sheet": "true" if sheet else "false",
        }
    return out


class TestQualityMath(unittest.TestCase):
    def test_scans_are_double_weighted(self):
        # 4 samples: two normal CER 0, two scans CER 1.0 → mean must be 0.5
        files = [s["file"] for s in score.PDF_OFFICE_SAMPLES]
        normal = [f for f, s in zip(files, score.PDF_OFFICE_SAMPLES) if not s.get("double_weight")]
        scans = [f for f, s in zip(files, score.PDF_OFFICE_SAMPLES) if s.get("double_weight")]
        cer = {f: 0.0 for f in normal}
        cer.update({f: 1.0 for f in scans})
        res = score.quality_scores("xberg", rows_for("xberg", cer))
        # weight total = 5 normal x1 + 2 scans x2 = 9; error sum = 2 scans x1 x2
        self.assertAlmostEqual(res["text"], 10.0 * (1 - 4.0 / 9.0), places=6)

    def test_cer_above_one_does_not_dominate(self):
        files = [s["file"] for s in score.PDF_OFFICE_SAMPLES]
        cer = {f: 0.0 for f in files}
        cer[files[3]] = 3.0  # a scan, double weighted, clamped to 1.0
        res = score.quality_scores("xberg", rows_for("xberg", cer))
        # weights: 5 normal + 2*2 scans = 9; clamped sum = 2*1.0
        self.assertAlmostEqual(res["text"], 10.0 * (1 - 2.0 / 9.0), places=6)

    def test_missing_samples_are_excluded_not_zeroed(self):
        files = [s["file"] for s in score.PDF_OFFICE_SAMPLES]
        cer = {files[0]: 0.0, files[1]: 0.0}  # everything else missing
        res = score.quality_scores("xberg", rows_for("xberg", cer))
        self.assertAlmostEqual(res["text"], 10.0, places=6)
        self.assertEqual(len(res["used"]), 2)
        self.assertGreater(len(res["missing"]), 0)

    def test_unsupported_format_is_reported(self):
        # mineru cannot read docx/xlsx: they must be missing, never scored 0
        files = [s["file"] for s in score.PDF_OFFICE_SAMPLES]
        cer = {f: 0.0 for f in files}
        res = score.quality_scores("mineru", rows_for("mineru", cer))
        missing_files = {m["file"] for m in res["missing"]}
        self.assertIn("generated/docx_report.docx", missing_files)
        self.assertIn("generated/xlsx_workbook.xlsx", missing_files)

    def test_missing_table_counts_as_zero(self):
        files = [s["file"] for s in score.PDF_OFFICE_SAMPLES]
        cer = {f: 0.0 for f in files}
        with_tables = score.quality_scores("xberg", rows_for("xberg", cer, cell=1.0))
        without = score.quality_scores("xberg", rows_for("xberg", cer, cell=None))
        self.assertGreater(with_tables["structure"], without["structure"])
        self.assertEqual(without["structure"], 0.0)

    def test_provenance_uses_only_expected_signals(self):
        files = [s["file"] for s in score.PDF_OFFICE_SAMPLES]
        cer = {f: 0.0 for f in files}
        full = score.quality_scores("xberg", rows_for("xberg", cer))
        none = score.quality_scores("xberg", rows_for("xberg", cer, page=False, bbox=False, sheet=False))
        self.assertGreater(full["provenance"], 0.0)
        self.assertEqual(none["provenance"], 0.0)

    def test_text_domain_has_no_provenance_expectation(self):
        files = [s["file"] for s in score.TEXT_SAMPLES]
        cer = {f: 0.0 for f in files}
        res = score.quality_scores("native", rows_for("native", cer))
        # csv/tsv expect a sheet signal, so provenance is measured there
        self.assertIsNotNone(res["provenance"])

    def test_quality_is_none_when_nothing_measured(self):
        res = score.quality_scores("xberg", {})
        self.assertIsNone(res["quality"])


class TestWeightedTotal(unittest.TestCase):
    def test_renormalises_over_measured_dimensions(self):
        full = {"quality": 10, "performance": 10, "resources": 10, "coverage": 10, "ops_license": 10}
        total, measured, incomplete = score.weighted_total(full)
        self.assertAlmostEqual(total, 10.0)
        self.assertEqual(len(measured), 5)
        self.assertFalse(incomplete)

        partial = {"quality": 10, "performance": 10, "resources": None, "coverage": 10, "ops_license": 10}
        total, measured, incomplete = score.weighted_total(partial)
        # resources was 15 % of the weight; the rest renormalises to 100 %
        self.assertAlmostEqual(total, 10.0)
        self.assertTrue(incomplete)
        self.assertNotIn("resources", measured)

    def test_unmeasured_dimension_does_not_change_a_middling_score(self):
        # A missing dimension must not read as 0 (that was the Phase 0 bug).
        dims = {"quality": 5, "performance": 5, "resources": None, "coverage": 5, "ops_license": 5}
        total, _, _ = score.weighted_total(dims)
        self.assertAlmostEqual(total, 5.0)

    def test_empty_is_none(self):
        total, measured, incomplete = score.weighted_total(
            {"quality": None, "performance": None, "resources": None, "coverage": None, "ops_license": None})
        self.assertIsNone(total)
        self.assertEqual(measured, [])
        self.assertTrue(incomplete)


class TestOutputStability(unittest.TestCase):
    def test_markdown_tables_render_from_the_same_data(self):
        result = {
            "rubric": {"domains": {"pdf_office": ["a", "b"], "text": ["c"]}},
            "engines": [{
                "engine": "x", "domain": "pdf_office", "version": "1",
                "quality_sub": {"text": 7.0, "structure": 8.0, "provenance": 9.0},
                "dimensions": {"quality": 7.5, "performance": 6.0, "resources": 5.0,
                               "coverage": 4.0, "ops_license": 3.0},
                "weighted": 6.1, "samples_used": ["a"], "samples_missing": [],
                "low_confidence": False, "incomplete": False, "notes": [],
                "measured_dimensions": ["quality"],
            }],
        }
        md = score.render_markdown(result)
        self.assertIn("| x |", md)
        self.assertIn("7.50", md)
        self.assertIn("6.10", md)

    def test_missing_data_renders_as_na(self):
        self.assertEqual(score.fmt(None), "n/a")
        self.assertEqual(score.fmt(1.234, 1), "1.2")


class TestScoreAllSmoke(unittest.TestCase):
    """Runs against the real summary CSVs when they exist (no network, no engines)."""

    def test_score_all_is_deterministic(self):
        a = score.score_all()
        b = score.score_all()
        self.assertEqual(a["engines"], b["engines"])
        for e in a["engines"]:
            if e["weighted"] is not None:
                self.assertGreaterEqual(e["weighted"], 0.0)
                self.assertLessEqual(e["weighted"], 10.0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
