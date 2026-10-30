#!/usr/bin/env python3
"""Generate ParseDock's synthetic benchmark fixtures.

Everything here is self-authored, non-sensitive content so the corpus can be
committed and shared. Each fixture records its SHA-256, size and a gold text
file used for CER/WER scoring.

Usage:  python gen_fixtures.py --out ../fixtures
Requires: reportlab, python-docx, openpyxl, pillow, pypdf
"""
import argparse
import hashlib
import json
import os
import random
from datetime import date, datetime
from pathlib import Path

from reportlab.lib import colors
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle, getSampleStyleSheet
from reportlab.lib.units import mm
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import (Image, PageBreak, Paragraph, SimpleDocTemplate,
                                Spacer, Table, TableStyle)

FONT_CANDIDATES = [
    ("/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", "NotoCJK", 0),
    ("/usr/share/fonts/truetype/arphic/uming.ttc", "UMing", 0),
]

EN_TITLE = "Quarterly Logistics Review"
EN_BODY = (
    "The pilot program moved 128,400 pallets across three regional hubs in the second "
    "quarter, a 12.5 percent increase over the same period last year. Average dock-to-stock "
    "time fell from 4.2 hours to 3.1 hours after the scheduling change, while the number of "
    "missed appointments dropped by 38 percent. Warehouse B remains the constraint: its "
    "inbound lane utilisation reached 94 percent during peak weeks."
)
EN_BODY2 = (
    "Two risks need attention. First, the cold-chain corridor depends on a single carrier and "
    "any interruption would delay roughly 900 shipments per week. Second, the new scanning "
    "stations are running firmware 2.4.1, which has a known issue with duplicate labels. "
    "A remediation plan is due on 15 November 2026 and will be tracked weekly."
)

ZH_TITLE = "供应链季度复盘报告"
ZH_BODY = (
    "本季度三个区域枢纽共处理托盘十二万八千四百个，同比增长百分之十二点五。"
    "调整排程后，平均卸货上架时间由四点二小时降至三点一小时，爽约次数下降百分之三十八。"
    "仓库B仍是瓶颈，高峰期入库通道利用率达到百分之九十四。"
)
ZH_BODY2 = (
    "需要关注两项风险。第一，冷链走廊依赖单一承运商，一旦中断每周约九百票货物将延误。"
    "第二，新扫描站运行的固件2.4.1存在标签重复的已知问题，整改计划应于二零二六年十一月十五日前提交。"
)

TABLE_ROWS = [
    ["Region", "Pallets", "On-time %", "Cost / pallet"],
    ["North", "48,120", "97.4", "3.85"],
    ["Central", "41,600", "95.1", "4.02"],
    ["South", "38,680", "93.8", "4.31"],
    ["Total", "128,400", "95.7", "4.03"],
]


def register_cjk_font():
    for path, name, idx in FONT_CANDIDATES:
        if os.path.exists(path):
            try:
                pdfmetrics.registerFont(TTFont(name, path, subfontIndex=idx))
                return name
            except Exception:
                continue
    return "Helvetica"


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def build_pdf(path, pages=3, two_column=False, chinese=False, with_table=True):
    font = register_cjk_font() if chinese else "Helvetica"
    styles = getSampleStyleSheet()
    title_style = ParagraphStyle("t", parent=styles["Title"], fontName=font, fontSize=18, leading=22)
    h2 = ParagraphStyle("h2", parent=styles["Heading2"], fontName=font, fontSize=13, leading=17)
    body = ParagraphStyle("b", parent=styles["BodyText"], fontName=font, fontSize=10.5, leading=15)
    doc = SimpleDocTemplate(str(path), pagesize=A4, leftMargin=18 * mm, rightMargin=18 * mm,
                            topMargin=18 * mm, bottomMargin=18 * mm,
                            title=EN_TITLE, author="ParseDock fixture generator")

    title, b1, b2 = (ZH_TITLE, ZH_BODY, ZH_BODY2) if chinese else (EN_TITLE, EN_BODY, EN_BODY2)
    story = [Paragraph(title, title_style), Spacer(1, 6)]
    for p in range(pages):
        if p > 0:
            story.append(PageBreak())
        story.append(Paragraph(f"Section {p + 1}: Operational detail", h2))
        story.append(Paragraph(f"Page marker {p + 1}. " + b1, body))
        story.append(Spacer(1, 4))
        story.append(Paragraph(b2, body))
        if with_table and p == 0:
            story.append(Spacer(1, 6))
            data = [[Paragraph(c, body) for c in row] for row in TABLE_ROWS]
            t = Table(data, colWidths=[35 * mm, 32 * mm, 30 * mm, 32 * mm])
            t.setStyle(TableStyle([
                ("GRID", (0, 0), (-1, -1), 0.4, colors.grey),
                ("BACKGROUND", (0, 0), (-1, 0), colors.whitesmoke),
                ("VALIGN", (0, 0), (-1, -1), "MIDDLE"),
            ]))
            story.append(t)
    doc.build(story)


def build_two_column_pdf(path):
    """Two-column layout rendered manually to test reading order."""
    from reportlab.pdfgen import canvas
    font = "Helvetica"
    c = canvas.Canvas(str(path), pagesize=A4)
    width, height = A4
    col_w = (width - 60) / 2
    paras = [
        ("Left column first paragraph. " + EN_BODY, 40),
        ("Left column second paragraph explains exceptions in the inbound schedule.", 40),
        ("Right column first paragraph. " + EN_BODY2, 40 + col_w),
        ("Right column second paragraph lists the owners for each remediation action.", 40 + col_w),
    ]
    c.setFont("Helvetica-Bold", 16)
    c.drawString(40, height - 60, "Dock Operations Memo")
    c.setFont(font, 9)
    for text, x in paras:
        t = c.beginText(x, height - 100)
        t.setFont(font, 9)
        line = ""
        for word in text.split():
            if len(line) + len(word) > 52:
                t.textLine(line)
                line = word
            else:
                line = (line + " " + word).strip()
        if line:
            t.textLine(line)
        c.drawText(t)
    c.showPage()
    c.save()


def render_text_image(text, path, size=(1654, 2339), font_path=None):
    from PIL import Image as PILImage, ImageDraw, ImageFont
    img = PILImage.new("RGB", size, "white")
    d = ImageDraw.Draw(img)
    f = ImageFont.truetype(font_path, 42) if font_path else ImageFont.load_default()
    y = 120
    for line in text.split("\n"):
        d.text((110, y), line, fill="black", font=f)
        y += 70
    img.save(path)
    return img


def build_scanned_pdf(path, img_path):
    from PIL import Image as PILImage
    img = PILImage.open(img_path).convert("RGB")
    img.save(path, "PDF", resolution=200.0)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(Path(__file__).resolve().parent.parent / "fixtures"))
    args = ap.parse_args()
    root = Path(args.out)
    gen = root / "generated"
    gold = root / "gold"
    bad = root / "invalid"
    for d in (gen, gold, bad):
        d.mkdir(parents=True, exist_ok=True)

    records = []

    def note(path: Path, category, fmt, gold_text=None, pages=0, notes=""):
        rel = path.relative_to(root).as_posix()
        if gold_text is not None:
            gp = gold / (path.stem + ".gold.txt")
            gp.write_text(gold_text, encoding="utf-8")
        records.append({
            "path": rel,
            "category": category,
            "format": fmt,
            "bytes": path.stat().st_size,
            "sha256": sha256(path),
            "pages": pages,
            "gold": (gold / (path.stem + ".gold.txt")).relative_to(root).as_posix() if gold_text is not None else None,
            "notes": notes,
            "sensitive": False,
        })

    # --- native-text PDFs -------------------------------------------------
    p = gen / "pdf_text_en_3p.pdf"
    build_pdf(p, pages=3)
    note(p, "pdf_native_en", "pdf", (EN_TITLE + "\n" + EN_BODY + "\n" + EN_BODY2) * 3, 3,
         "single column, header/footer free, one table")

    p = gen / "pdf_text_zh_2p.pdf"
    build_pdf(p, pages=2, chinese=True)
    note(p, "pdf_native_zh", "pdf", (ZH_TITLE + "\n" + ZH_BODY + "\n" + ZH_BODY2) * 2, 2,
         "CJK native text, paragraph segmentation")

    p = gen / "pdf_two_column_en.pdf"
    build_two_column_pdf(p)
    note(p, "pdf_two_column", "pdf", EN_BODY + "\n" + EN_BODY2, 1, "reading-order sensitive")

    # --- scanned / image PDF ---------------------------------------------
    img_txt = ("INVOICE 2026-0042\nSupplier: Northwind Logistics\n"
               "Total amount: 18,420.50 EUR\nDue date: 2026-11-30\n"
               "Payment terms: net 30 days\nReference: dock-ops-q3")
    img_path = gen / "scan_source_en.png"
    img = render_text_image(img_txt, img_path)
    note(img_path, "image_png", "png", img_txt, 1, "clean 200dpi-style render")
    sp = gen / "pdf_scanned_en.pdf"
    build_scanned_pdf(sp, img_path)
    note(sp, "pdf_scanned", "pdf", img_txt, 1, "single page image-only PDF, OCR required")

    zh_img_txt = "采购订单\n供应商：华北物流有限公司\n金额：人民币 128,400 元\n交付日期：2026年11月15日"
    zh_font = None
    for cand, _, _ in FONT_CANDIDATES:
        if os.path.exists(cand):
            zh_font = cand
            break
    zh_img = gen / "scan_source_zh.png"
    if zh_font:
        render_text_image(zh_img_txt, zh_img, font_path=zh_font)
        note(zh_img, "image_png_zh", "png", zh_img_txt, 1, "CJK OCR")
        zh_pdf = gen / "pdf_scanned_zh.pdf"
        build_scanned_pdf(zh_pdf, zh_img)
        note(zh_pdf, "pdf_scanned_zh", "pdf", zh_img_txt, 1, "CJK image-only PDF")

    # --- DOCX -------------------------------------------------------------
    import docx
    from docx.shared import Inches, Pt
    d = docx.Document()
    d.add_heading("Dock Throughput Review", level=1)
    d.add_paragraph(EN_BODY)
    d.add_heading("Findings", level=2)
    d.add_paragraph("Three findings stand out from the quarter:", style="List Bullet")
    for item in ["Inbound lane utilisation peaked at 94 percent.",
                 "Duplicate labels appeared on 0.7 percent of shipments.",
                 "Cold-chain carrier concentration remains unmitigated."]:
        d.add_paragraph(item, style="List Bullet")
    d.add_heading("Regional table", level=2)
    t = d.add_table(rows=len(TABLE_ROWS), cols=4)
    t.style = "Table Grid"
    for r, row in enumerate(TABLE_ROWS):
        for c, val in enumerate(row):
            t.cell(r, c).text = val
    d.add_paragraph(EN_BODY2)
    d.sections[0].header.paragraphs[0].text = "ParseDock benchmark fixture"
    docx_path = gen / "docx_report.docx"
    d.save(docx_path)
    note(docx_path, "docx", "docx",
         "Dock Throughput Review\n" + EN_BODY + "\nFindings\n" + EN_BODY2, 0,
         "headings, bullets, table, page header")

    # --- XLSX -------------------------------------------------------------
    import openpyxl
    wb = openpyxl.Workbook()
    ws = wb.active
    ws.title = "Summary"
    ws["A1"] = "Region"
    ws["B1"] = "Pallets"
    ws["C1"] = "On-time %"
    ws["D1"] = "Cost"
    for i, row in enumerate(TABLE_ROWS[1:], start=2):
        for j, val in enumerate(row, start=1):
            ws.cell(row=i, column=j, value=val if j == 1 else (
                float(val.replace(",", "")) if j == 2 else float(val)))
    ws["A7"] = "Formula check"
    ws["B7"] = "=SUM(B2:B5)"
    ws["C7"] = "=AVERAGE(C2:C5)"
    ws.merge_cells("A9:D9")
    ws["A9"] = "Merged note: figures unaudited"
    ws2 = wb.create_sheet("Detail")
    ws2["A1"] = "Date"
    ws2["B1"] = "Shipments"
    ws2["C1"] = "Notes"
    ws2.append([date(2026, 7, 1), 1830, "baseline"])
    ws2.append([date(2026, 8, 1), 1942, "peak week 33"])
    ws2.append([datetime(2026, 9, 1, 8, 30), 1801, "firmware rollback"])
    ws2["B5"] = "=SUM(B2:B4)"
    ws2["E1"] = "Precision"
    ws2["E2"] = 128400.125
    xlsx_path = gen / "xlsx_workbook.xlsx"
    wb.save(xlsx_path)
    note(xlsx_path, "xlsx", "xlsx",
         "Summary Region Pallets On-time Cost Formula check 128400 95.7 Detail 2026-07-01 1830", 2,
         "2 sheets, formulas, merged cell, dates, float precision")

    # --- CSV / TSV / TXT / MD --------------------------------------------
    csv_text = "region,pallets,on_time_pct,cost_per_pallet\n" + \
               "North,48120,97.4,3.85\nCentral,41600,95.1,4.02\n" + \
               "South,38680,93.8,4.31\nTotal,128400,95.7,4.03\n"
    p = gen / "csv_utf8.csv"
    p.write_text(csv_text, encoding="utf-8")
    note(p, "csv", "csv", csv_text, 0, "UTF-8, comma, clean")

    p = gen / "csv_gb18030.csv"
    p.write_bytes(csv_text.replace("region", "区域").encode("gb18030"))
    note(p, "csv_gb18030", "csv", csv_text.replace("region", "区域"), 0, "GB18030 encoded header")

    p = gen / "csv_semicolon_eu.csv"
    eu = "region;pallets;cost\nNorth;48120;3,85\nCentral;41600;4,02\n"
    p.write_text(eu, encoding="utf-8")
    note(p, "csv", "csv", eu, 0, "semicolon delimiter, decimal comma")

    p = gen / "tsv_data.tsv"
    tsv = "region\tpallets\ton_time\nNorth\t48120\t97.4\nCentral\t41600\t95.1\n"
    p.write_text(tsv, encoding="utf-8")
    note(p, "tsv", "tsv", tsv, 0, "tab separated")

    p = gen / "txt_plain.txt"
    txt = "Dock memo\n\n" + EN_BODY + "\n\n" + EN_BODY2 + "\n"
    p.write_text(txt, encoding="utf-8")
    note(p, "txt", "txt", txt, 0, "plain text, blank-line paragraphs")

    p = gen / "md_notes.md"
    md = ("# Dock notes\n\n" + EN_BODY + "\n\n## Table\n\n"
          "| Region | Pallets |\n| --- | --- |\n| North | 48120 |\n| South | 38680 |\n\n"
          "## Checklist\n\n- [ ] firmware rollback\n- [x] carrier review\n")
    p.write_text(md, encoding="utf-8")
    note(p, "md", "md", md, 0, "markdown with table and task list")

    # --- Images ----------------------------------------------------------
    from PIL import Image as PILImage
    shot = PILImage.new("RGB", (900, 420), "white")
    from PIL import ImageDraw
    dr = ImageDraw.Draw(shot)
    dr.rectangle([20, 20, 880, 400], outline="black", width=3)
    dr.text((50, 60), "Dashboard: 128,400 pallets shipped", fill="black")
    dr.text((50, 110), "On-time 95.7% | Missed 41", fill="black")
    jpg = gen / "image_screenshot.jpg"
    shot.save(jpg, "JPEG", quality=85)
    note(jpg, "image_jpeg", "jpg", "Dashboard: 128,400 pallets shipped On-time 95.7% Missed 41", 1,
         "screenshot-like JPEG")
    webp = gen / "image_screenshot.webp"
    shot.save(webp, "WEBP", quality=85)
    note(webp, "image_webp", "webp", "Dashboard: 128,400 pallets shipped On-time 95.7% Missed 41", 1,
         "same content as JPEG in WebP")

    # --- invalid / adversarial -------------------------------------------
    p = bad / "empty.pdf"
    p.write_bytes(b"")
    note(p, "invalid", "pdf", None, 0, "zero-byte file")

    p = bad / "corrupt.pdf"
    p.write_bytes(b"%PDF-1.7\n" + bytes(random.randrange(32, 126) for _ in range(4096)))
    note(p, "invalid", "pdf", None, 0, "truncated/garbage PDF body")

    p = bad / "renamed_docx.pdf"
    p.write_bytes((gen / "docx_report.docx").read_bytes())
    note(p, "invalid", "pdf", None, 0, "DOCX bytes with a .pdf extension")

    p = bad / "password.pdf"
    from pypdf import PdfWriter
    w = PdfWriter()
    w.add_blank_page(width=595, height=842)
    w.encrypt("secret")
    with open(p, "wb") as fh:
        w.write(fh)
    note(p, "invalid", "pdf", None, 1, "AES-encrypted PDF")

    # Zip bomb: 40 MiB of zeros compressed inside a fake docx.
    import zipfile
    p = bad / "zipbomb.docx"
    with zipfile.ZipFile(p, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        z.writestr("word/document.xml", b"<w:document/>")
        z.writestr("word/media/blob.bin", b"\0" * (40 << 20))
    note(p, "invalid", "docx", None, 0, "deflate bomb, ratio ~1000:1")

    p = bad / "not_a_docx.docx"
    p.write_bytes(b"PK\x03\x04" + b"garbage" * 100)
    note(p, "invalid", "docx", None, 0, "zip magic with invalid central directory")

    manifest = root / "manifest.json"
    manifest.write_text(json.dumps({
        "generated_by": "benchmark/scripts/gen_fixtures.py",
        "generated_at": datetime.utcnow().isoformat() + "Z",
        "fixtures": records,
    }, indent=2, ensure_ascii=False), encoding="utf-8")

    by_cat = {}
    for r in records:
        by_cat[r["category"]] = by_cat.get(r["category"], 0) + 1
    print(f"wrote {len(records)} fixtures under {root}")
    for k, v in sorted(by_cat.items()):
        print(f"  {k:22s} {v}")


if __name__ == "__main__":
    main()
