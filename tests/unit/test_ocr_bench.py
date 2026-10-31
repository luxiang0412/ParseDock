"""Scoring helpers used by the OCR spike (benchmark/scripts/ocr_bench.py).

The benchmark itself needs the engine binaries, a GPU and ~10 minutes; these
tests pin the arithmetic so a reported CER means the same thing after the
script is edited. Keep this file dependency-free (stdlib only) so it runs in
the normal unit job.
"""

import importlib.util
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "benchmark" / "scripts" / "ocr_bench.py"


def load():
    spec = importlib.util.spec_from_file_location("ocr_bench_under_test", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)  # type: ignore[union-attr]
    return module


class NormalisationTest(unittest.TestCase):
    def setUp(self):
        self.bench = load()

    def test_squash_removes_all_whitespace(self):
        self.assertEqual(self.bench.squash("采购 订单\n供 应 商  :"), "采购订单供应商:")

    def test_levenshtein_basics(self):
        lev = self.bench.levenshtein
        self.assertEqual(lev("", ""), 0)
        self.assertEqual(lev("abc", "abc"), 0)
        self.assertEqual(lev("", "abc"), 3)
        self.assertEqual(lev("abc", ""), 3)
        self.assertEqual(lev("kitten", "sitting"), 3)


class CerTest(unittest.TestCase):
    def setUp(self):
        self.bench = load()

    def test_identical_is_zero(self):
        self.assertAlmostEqual(self.bench.cer("季度物流复核报告", "季度物流复核报告"), 0.0)

    def test_whitespace_is_ignored(self):
        # This is the tesseract failure mode: right characters, wrong spacing.
        self.assertAlmostEqual(self.bench.cer("供 应 商 : 华北", "供应商:华北"), 0.0)

    def test_one_substitution_over_length(self):
        self.assertAlmostEqual(self.bench.cer("1990", "1991"), 0.25)

    def test_empty_gold_is_zero(self):
        self.assertAlmostEqual(self.bench.cer("anything", ""), 0.0)

    def test_hallucination_is_bounded_at_one_only_when_gold_is_short(self):
        # 4 inserted characters on a 4 character gold: distance 4, ratio 1.0
        self.assertAlmostEqual(self.bench.cer("abcd", "wxyz"), 1.0)


class DigitCerTest(unittest.TestCase):
    def setUp(self):
        self.bench = load()

    def test_only_digits_are_compared(self):
        # text is wrong, the numbers are right -> digit CER 0
        self.assertAlmostEqual(self.bench.digit_cer("Total: 18,420.50 EUR", "Summe 18,420.50 EUR"), 0.0)

    def test_missing_digit_sequence_hurts(self):
        # gold has 6 digits, hypothesis has 4 -> 2/6
        self.assertAlmostEqual(self.bench.digit_cer("1234", "123456"), 2 / 6)

    def test_no_digits_in_gold(self):
        self.assertAlmostEqual(self.bench.digit_cer("abc", "def"), 0.0)


class WerTest(unittest.TestCase):
    def setUp(self):
        self.bench = load()

    def test_punctuation_and_case_are_folded(self):
        self.assertAlmostEqual(self.bench.wer("Supplier. Northwind logistics", "Supplier: Northwind Logistics"), 0.0)

    def test_one_wrong_token(self):
        self.assertAlmostEqual(self.bench.wer("Supplier Northwind", "Supplier Northwind Ltd"), 1 / 3)


class PercentileTest(unittest.TestCase):
    def setUp(self):
        self.bench = load()

    def test_median_and_p95_of_small_samples(self):
        values = [10.0, 20.0, 30.0, 40.0, 50.0]
        self.assertAlmostEqual(self.bench.pct(values, 0.5), 30.0)
        self.assertAlmostEqual(self.bench.pct(values, 0.95), 48.0)

    def test_single_sample(self):
        self.assertAlmostEqual(self.bench.pct([7.0], 0.95), 7.0)
        self.assertAlmostEqual(self.bench.pct([], 0.95), 0.0)


class FixtureManifestTest(unittest.TestCase):
    """The OCR fixtures must be committed with their hashes and gold text."""

    def test_manifest_matches_gold(self):
        import json

        fixtures = ROOT / "benchmark" / "fixtures" / "ocr"
        manifest = json.loads((fixtures / "MANIFEST.json").read_text(encoding="utf-8"))
        self.assertTrue(manifest, "ocr MANIFEST.json is empty")
        for row in manifest:
            name = row["fixture"]
            gold = (fixtures / f"{name}.gold.txt").read_text(encoding="utf-8")
            self.assertEqual(len(gold.splitlines()), row["lines"], f"{name}: line count drifted")
            self.assertTrue((fixtures / f"{name}.p1.gold.txt").exists(), f"{name}: missing page-1 gold")
            self.assertRegex(row["sha256"], r"^[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
