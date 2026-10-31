#!/usr/bin/env python3
"""Generate deterministic OCR benchmark fixtures (image-only PDFs + gold text).

Why: the pre-existing scan fixtures (``pdf_scanned_{en,zh}.pdf``) hold six short
lines on an otherwise empty A4 page. They are enough to prove that OCR runs, not
enough to rank backends: layout heuristics, line segmentation and character
recognition all get an easy ride. These fixtures are dense, multi-line, and
contain the things that actually break OCR — digits, dates, amounts, mixed
punctuation, and CJK without spaces.

Everything here is fictional and generated locally. No external material is
used, so the files can live in the repository; the fonts are OFL/Apache and
already required by ``gen_fixtures.py``.

Two variants per language:

    <lang>_dense.png/pdf    2 pages, ~40 lines per page — quality + latency
    <lang>_line.png         1 line, tight crop            — latency floor

Gold text (``*.gold.txt``) is the exact string drawn on the page, one line per
line, which is what ``ocr_bench.py`` scores against.

Usage: python3 benchmark/scripts/gen_ocr_fixtures.py [--out DIR] [--dpi N]
"""

from __future__ import annotations

import argparse
import hashlib
import random
import unicodedata
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

# Fictional content. Kept boring on purpose: no names, no real organisations.
EN_LINES = [
    "Quarterly Logistics Review",
    "Invoice number: 2026-0042",
    "Supplier: Northwind Logistics Ltd",
    "Total amount: 18,420.50 EUR",
    "Due date: 2026-11-30",
    "Payment terms: net 30 days",
    "Reference: dock-ops-q3",
    "Pallet count: 1,284",
    "On-time delivery: 96.4 percent",
    "Average dock-to-stock: 4.2 hours",
    "Damaged units: 37 of 12,840",
    "Cold-chain excursions: 2",
    "Warehouse B inbound lane utilisation: 94 percent",
    "Freight cost per pallet: 3.85 EUR",
    "Customs clearance: 2026-11-02",
    "Container: MSKU 7741923",
    "Seal: CN-448192",
    "Incoterm: DAP Rotterdam",
    "Insurance: 0.35 percent of CIF",
    "Claim reference: CL-2026-017",
    "Audit trail: batch 17, page 3 of 9",
    "Signature: /s/ A. Sorensen",
    "Checked by: quality desk, 2026-10-28",
    "Filing code: LOG/Q3/2026/0042",
]

ZH_LINES = [
    "季度物流复核报告",
    "发票编号：2026-0042",
    "供应商：华北物流有限公司",
    "总金额：人民币 18,420.50 元",
    "付款期限：2026年11月30日",
    "账期：净30天",
    "合同编号：DOCK-OPS-Q3",
    "托盘数量：1,284 个",
    "准时交付率：96.4%",
    "平均入库时长：4.2 小时",
    "破损数量：37 件（共 12,840 件）",
    "冷链异常：2 次",
    "B 仓入库通道利用率：94%",
    "单托运费：3.85 元",
    "报关完成日期：2026年11月2日",
    "集装箱号：MSKU 7741923",
    "封条号：CN-448192",
    "贸易术语：DAP 鹿特丹",
    "保险费率：CIF 的 0.35%",
    "索赔编号：CL-2026-017",
    "审计记录：第17批，第3页，共9页",
    "签署：/s/ 李伟",
    "复核人：质量岗，2026年10月28日",
    "归档编号：LOG/Q3/2026/0042",
]

FONT_CANDIDATES = [
    ("/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", 0),
    ("/usr/share/fonts/truetype/arphic/uming.ttc", 0),
    ("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", 0),
]

SEED = 20261031


def pick_font(size: int) -> ImageFont.FreeTypeFont:
    for path, index in FONT_CANDIDATES:
        if Path(path).exists():
            try:
                return ImageFont.truetype(path, size, index=index)
            except OSError:
                continue
    raise SystemExit("no usable font found; install fonts-noto-cjk")


def render_page(lines: list[str], dpi: int, width_in: float = 8.27, height_in: float = 11.69):
    """Draw ``lines`` top-to-bottom on a white page and return a PIL image."""
    w, h = int(width_in * dpi), int(height_in * dpi)
    img = Image.new("RGB", (w, h), "white")
    draw = ImageDraw.Draw(img)
    font = pick_font(max(10, int(dpi * 0.155)))  # ~11 pt
    x, y = int(dpi * 0.9), int(dpi * 0.9)
    step = int(dpi * 0.26)
    for line in lines:
        draw.text((x, y), line, fill="black", font=font)
        y += step
    return img


TABLE_ROWS = [
    ["区域", "托盘数", "准时率", "单托运费"],
    ["华北", "48,120", "97.4%", "3.85"],
    ["华东", "41,600", "95.1%", "4.02"],
    ["华南", "38,680", "93.8%", "4.31"],
    ["合计", "128,400", "95.7%", "4.03"],
]


def render_table_page(dpi: int, width_in: float = 8.27, height_in: float = 11.69):
    """Draw a bordered table so xberg has real table geometry to report."""
    w, h = int(width_in * dpi), int(height_in * dpi)
    img = Image.new("RGB", (w, h), "white")
    draw = ImageDraw.Draw(img)
    font = pick_font(max(10, int(dpi * 0.13)))
    left, top = int(dpi * 0.9), int(dpi * 1.2)
    col_w, row_h = int(dpi * 1.6), int(dpi * 0.32)
    rows, cols = len(TABLE_ROWS), len(TABLE_ROWS[0])
    draw.text((left, int(dpi * 0.7)), "季度物流汇总表", fill="black", font=pick_font(max(12, int(dpi * 0.18))))
    for r in range(rows + 1):
        y = top + r * row_h
        draw.line([(left, y), (left + cols * col_w, y)], fill="black", width=2)
    for c in range(cols + 1):
        x = left + c * col_w
        draw.line([(x, top), (x, top + rows * row_h)], fill="black", width=2)
    for r, row in enumerate(TABLE_ROWS):
        for c, cell in enumerate(row):
            draw.text((left + c * col_w + int(dpi * 0.06), top + r * row_h + int(dpi * 0.06)),
                      cell, fill="black", font=font)
    return img


def normalise(text: str) -> str:
    return unicodedata.normalize("NFC", text)


FIXED_PDF_DATE = b"D:20260101000000Z"


def stabilise_pdf(path: Path) -> None:
    """Rewrite the embedded timestamps so two runs produce identical bytes.

    PIL stamps ``/CreationDate`` (and sometimes ``/ModDate``) with the current
    time, which makes a "reproducible" fixture change hash on every run. The
    replacement keeps the same length, so the cross-reference offsets stay valid.
    """
    data = path.read_bytes()
    out = bytearray()
    i = 0
    needle = b"D:"
    while True:
        j = data.find(needle, i)
        if j < 0:
            out += data[i:]
            break
        out += data[i:j]
        k = j + 2
        # 14 digits, optionally followed by Z
        while k < len(data) and k - (j + 2) < 14 and data[k : k + 1].isdigit():
            k += 1
        if k - (j + 2) == 14:
            out += FIXED_PDF_DATE
            if data[k : k + 1] == b"Z":
                k += 1
            i = k
        else:
            out += data[j:k]
            i = k
    if bytes(out) != data:
        path.write_bytes(bytes(out))


def write_fixture(out: Path, name: str, pages: list[list[str]], dpi: int) -> dict:
    images = [render_page(page, dpi) for page in pages]
    png = out / f"{name}.png"
    pdf = out / f"{name}.pdf"
    images[0].save(png, format="PNG", optimize=True)
    images[0].save(pdf, format="PDF", resolution=float(dpi), save_all=True, append_images=images[1:])
    stabilise_pdf(pdf)
    gold = "\n".join(normalise(line) for page in pages for line in page) + "\n"
    gpath = out / f"{name}.gold.txt"
    gpath.write_text(gold, encoding="utf-8")
    # Image-only runs see page 1 alone, so they are scored against page 1 only.
    (out / f"{name}.p1.gold.txt").write_text(
        "\n".join(normalise(line) for line in pages[0]) + "\n", encoding="utf-8"
    )
    return {
        "fixture": name,
        "pages": len(pages),
        "lines": sum(len(p) for p in pages),
        "chars": len(gold),
        "dpi": dpi,
        "png_bytes": png.stat().st_size,
        "pdf_bytes": pdf.stat().st_size,
        "sha256": hashlib.sha256(pdf.read_bytes()).hexdigest(),
        "sha256_png": hashlib.sha256(png.read_bytes()).hexdigest(),
    }


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="benchmark/fixtures/ocr")
    ap.add_argument("--dpi", type=int, default=300)
    args = ap.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    rng = random.Random(SEED)
    # Two dense pages: the base list plus a deterministic numeric variant, so a
    # backend cannot win by memorising 24 fixed strings.
    def page(base: list[str], page_no: int) -> list[str]:
        rows = []
        for i, line in enumerate(base):
            rows.append(line)
            if i % 4 == 3:
                n = rng.randint(1000, 9999)
                rows.append(f"Row {page_no:02d}-{i:02d}: {n:,} units, ref {n * 3 % 9973:04d}")
        return rows

    manifest = []
    for lang, base in (("en", EN_LINES), ("zh", ZH_LINES)):
        pages = [page(base, 1), page(base, 2)]
        manifest.append(write_fixture(out, f"{lang}_dense", pages, args.dpi))
        manifest.append(write_fixture(out, f"{lang}_line", [[base[0], base[3]]], args.dpi))

    # A rotated page: the one degradation where tesseract's page confidence does
    # move (0.26-0.31) and where the accurate engine recovers the text exactly,
    # which is what the PR-B escalation trigger is calibrated on.
    base = Image.open(out / "zh_dense.png").convert("RGB")
    rotated = base.rotate(90, expand=True)
    rotated.save(out / "zh_rot90.png", format="PNG", optimize=True)
    # Same page, rotated: the gold is the unrotated page-1 text.
    (out / "zh_rot90.gold.txt").write_text(
        (out / "zh_dense.p1.gold.txt").read_text(encoding="utf-8"), encoding="utf-8")
    (out / "zh_rot90.p1.gold.txt").write_text(
        (out / "zh_dense.p1.gold.txt").read_text(encoding="utf-8"), encoding="utf-8")
    manifest.append({
        "fixture": "zh_rot90",
        "pages": 1,
        "lines": 30,
        "chars": (out / "zh_dense.p1.gold.txt").read_text(encoding="utf-8").__len__(),
        "dpi": args.dpi,
        "png_bytes": (out / "zh_rot90.png").stat().st_size,
        "pdf_bytes": 0,
        "sha256": hashlib.sha256((out / "zh_rot90.png").read_bytes()).hexdigest(),
        "sha256_png": hashlib.sha256((out / "zh_rot90.png").read_bytes()).hexdigest(),
        "rotated_deg": 90,
    })

    # A page with an actual ruled table: the only way to see whether xberg's
    # table bounding boxes are in PDF points or in raster pixels.
    table_pages = [TABLE_ROWS, TABLE_ROWS]
    images = [render_table_page(args.dpi) for _ in table_pages]
    png, pdf = out / "zh_table.png", out / "zh_table.pdf"
    images[0].save(png, format="PNG", optimize=True)
    images[0].save(pdf, format="PDF", resolution=float(args.dpi), save_all=True, append_images=images[1:])
    stabilise_pdf(pdf)
    gold = "\n".join(" ".join(row) for row in TABLE_ROWS) + "\n"
    (out / "zh_table.gold.txt").write_text(gold, encoding="utf-8")
    (out / "zh_table.p1.gold.txt").write_text(gold, encoding="utf-8")
    manifest.append({
        "fixture": "zh_table",
        "pages": len(table_pages),
        "lines": len(TABLE_ROWS),
        "chars": len(gold),
        "dpi": args.dpi,
        "png_bytes": png.stat().st_size,
        "pdf_bytes": pdf.stat().st_size,
        "sha256": hashlib.sha256(pdf.read_bytes()).hexdigest(),
        "table": True,
    })

    import json

    (out / "MANIFEST.json").write_text(json.dumps(manifest, indent=1) + "\n", encoding="utf-8")
    for row in manifest:
        print(
            f"{row['fixture']:<14} pages={row['pages']} lines={row['lines']:<4} "
            f"chars={row['chars']:<5} png={row['png_bytes'] // 1024}KiB pdf={row['pdf_bytes'] // 1024}KiB "
            f"sha256={row['sha256'][:16]}"
        )


if __name__ == "__main__":
    main()
