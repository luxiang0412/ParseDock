#!/usr/bin/env python3
"""Parse a directory of documents and export readable results for comparison.

For every accepted file it uploads the document, waits for the result, and writes

    <out>/<slug>.md     the markdown the API serves (exact bytes)
    <out>/<slug>.json   the Document IR (elements, tables, routing, metrics)
    <out>/index.html    side-by-side index: metadata, a link to the ORIGINAL file
                        (file:// so it opens in your viewer), and the rendered markdown

Nothing is ACKed, so the results stay available on the server for the TTL you configured.
This runs entirely locally: original paths and document text stay under --out, which should
be a git-ignored directory (runtime/…). Never commit the output of a private corpus.

    scripts/export_markdown.py --dir "test_data/某目录" --out runtime/compare \
        --api http://127.0.0.1:18090 --token KEY
"""
from __future__ import annotations

import html
import json
import mimetypes
import os
import re
import sys
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ACCEPTED = {".pdf", ".docx", ".xlsx", ".xlsm", ".pptx", ".csv", ".tsv", ".txt", ".md",
            ".markdown", ".png", ".jpg", ".jpeg", ".webp", ".html", ".htm",
            ".doc", ".dot", ".xls", ".xlt", ".ppt", ".pot"}


def multipart(name: str, content: bytes, fields: dict[str, str]):
    b = "----" + uuid.uuid4().hex
    out = bytearray()
    for k, v in fields.items():
        out += f"--{b}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
    out += (f"--{b}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
            f"Content-Type: {mimetypes.guess_type(name)[0] or 'application/octet-stream'}\r\n\r\n").encode()
    out += content + f"\r\n--{b}--\r\n".encode()
    return bytes(out), {"Content-Type": f"multipart/form-data; boundary={b}"}


def http(api: str, method: str, path: str, body: bytes | None = None, headers=None, timeout=600):
    req = urllib.request.Request(api + path, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def download(api: str, path: str, headers, dest: Path) -> bool:
    """Stream a result body to disk: a large sheet can render to hundreds of MB."""
    req = urllib.request.Request(api + path, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=900) as r, dest.open("wb") as fh:
            while True:
                chunk = r.read(1 << 20)
                if not chunk:
                    break
                fh.write(chunk)
        return True
    except urllib.error.HTTPError:
        return False


def slug(root: Path, p: Path) -> str:
    rel = p.relative_to(root).as_posix()
    s = re.sub(r"[^\w.\-]+", "_", rel).strip("_")
    return s[:120]


MAX_RENDER_LINES = 20000
MAX_TABLE_ROWS = 1000


def render_md(md: str) -> str:
    """A deliberately small Markdown renderer: headings, paragraphs, tables, lists, code."""
    out, i, lines = [], 0, md.splitlines()[:MAX_RENDER_LINES]
    while i < len(lines):
        line = lines[i]
        if line.startswith("```"):
            buf = []
            i += 1
            while i < len(lines) and not lines[i].startswith("```"):
                buf.append(html.escape(lines[i]))
                i += 1
            out.append("<pre class=code>" + "\n".join(buf) + "</pre>")
            i += 1
            continue
        if line.startswith("|") and i + 1 < len(lines) and set(lines[i + 1].replace("|", "").strip()) <= set("-: "):
            header = [c.strip() for c in line.strip().strip("|").split("|")]
            i += 2
            rows, dropped = [], 0
            while i < len(lines) and lines[i].startswith("|"):
                if len(rows) < MAX_TABLE_ROWS:
                    rows.append([c.strip() for c in lines[i].strip().strip("|").split("|")])
                else:
                    dropped += 1
                i += 1
            t = ["<table><thead><tr>" + "".join(f"<th>{html.escape(c)}</th>" for c in header) + "</tr></thead><tbody>"]
            for r in rows:
                t.append("<tr>" + "".join(f"<td>{html.escape(c)}</td>" for c in r) + "</tr>")
            t.append("</tbody></table>")
            if dropped:
                t.append(f'<p class=warn>{dropped} more rows — open the .md file for the rest.</p>')
            out.append("".join(t))
            continue
        m = re.match(r"^(#{1,6})\s+(.*)$", line)
        if m:
            n = len(m.group(1))
            out.append(f"<h{n}>{html.escape(m.group(2))}</h{n}>")
            i += 1
            continue
        m = re.match(r"^[-*]\s+(.*)$", line)
        if m:
            items = []
            while i < len(lines) and re.match(r"^[-*]\s+", lines[i]):
                items.append(html.escape(re.sub(r"^[-*]\s+", "", lines[i])))
                i += 1
            out.append("<ul>" + "".join(f"<li>{x}</li>" for x in items) + "</ul>")
            continue
        if line.strip() == "":
            i += 1
            continue
        buf = []
        while i < len(lines) and lines[i].strip() and not lines[i].startswith(("#", "|", "```", "- ", "* ")):
            buf.append(html.escape(lines[i]))
            i += 1
        if not buf:
            # A line that matched one of the markers above but did not form that construct
            # (e.g. a stray "|" without a separator row) must still consume its line, or the
            # loop spins forever.
            buf.append(html.escape(line))
            i += 1
        out.append("<p>" + "<br>".join(buf) + "</p>")
    return "\n".join(out)


def main() -> int:
    ap = __import__("argparse").ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--api", default="http://127.0.0.1:18090")
    ap.add_argument("--token", required=True)
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--mode", default="auto", choices=["auto", "fast", "accurate"])
    ap.add_argument("--resume", action="store_true",
                    help="reuse <out>/<slug>.md|.json that already exist instead of re-parsing")
    args = ap.parse_args()

    root, out = Path(args.dir).resolve(), Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    auth = {"Authorization": f"Bearer {args.token}"}
    files = [p for p in sorted(root.rglob("*"))
             if p.is_file() and p.suffix.lower() in ACCEPTED and not p.name.startswith("~$")]
    if args.limit:
        files = files[: args.limit]
    entries = []
    for n, p in enumerate(files, 1):
        name = slug(root, p)
        rec = {"file": str(p), "name": p.name, "size": p.stat().st_size, "ext": p.suffix.lower(),
               "md": None, "json": None, "status": None, "error": None, "job": None}
        if args.resume and (out / f"{name}.md").exists():
            rec.update(status="completed (reused)", md=f"{name}.md",
                       md_bytes=(out / f"{name}.md").stat().st_size)
            if (out / f"{name}.json").exists():
                rec["json"] = f"{name}.json"
                rec["json_bytes"] = (out / f"{name}.json").stat().st_size
            entries.append(rec)
            print(f"[{n}/{len(files)}] {p.name}: reused", flush=True)
            continue
        body, h = multipart(p.name, p.read_bytes(), {"mode": args.mode})
        h.update(auth)
        code, resp = http(args.api, "POST", "/v1/jobs", body, h)
        if code not in (200, 202):
            rec.update(status="rejected", error=resp.decode("utf-8", "replace")[:300])
            entries.append(rec)
            print(f"[{n}/{len(files)}] {p.name}: rejected ({code})", flush=True)
            continue
        job = json.loads(resp)["job_id"]
        rec["job"] = job
        deadline = time.time() + 900
        st = {}
        while time.time() < deadline:
            code, resp = http(args.api, "GET", f"/v1/jobs/{job}", headers=auth)
            if code == 200:
                st = json.loads(resp)
                if st.get("status") in ("completed", "failed", "cancelled"):
                    break
            time.sleep(0.4)
        rec.update(status=st.get("status"), error=st.get("error_code"),
                   engine=st.get("engine"), pages=st.get("page_count"),
                   warnings=st.get("warning_count"), reason=st.get("routing_reason"))
        if st.get("status") == "completed":
            if download(args.api, f"/v1/jobs/{job}/result?format=markdown", auth, out / f"{name}.md"):
                rec["md"] = f"{name}.md"
                rec["md_bytes"] = (out / f"{name}.md").stat().st_size
            if download(args.api, f"/v1/jobs/{job}/result?format=json", auth, out / f"{name}.json"):
                rec["json"] = f"{name}.json"
                rec["json_bytes"] = (out / f"{name}.json").stat().st_size
                try:
                    if rec["json_bytes"] > 8_000_000:
                        raise MemoryError("IR too large to summarise inline")
                    with (out / f"{name}.json").open("rb") as fh:
                        ir = json.load(fh)
                    m = ir.get("metrics") or {}
                    rec.update(chars=m.get("output_chars"), parse_ms=m.get("parse_ms"),
                               tables=len(ir.get("tables") or []))
                except (json.JSONDecodeError, MemoryError, OSError):
                    pass
        entries.append(rec)
        print(f"[{n}/{len(files)}] {p.name}: {rec['status']} {rec.get('engine','')} "
              f"{rec.get('chars','')} chars {rec.get('tables','')} tables", flush=True)

    # index.html: original (file:// link) next to the rendered markdown
    rows = []
    for e in entries:
        # resolve() so a symlinked selection points at the real document
        orig = Path(e["file"]).resolve().as_uri()
        meta = (f"{e['size']/1024:.0f} KiB · {e.get('pages') or '-'} pages · "
                f"{e.get('engine') or '-'} · {e.get('chars') or '-'} chars · "
                f"{e.get('tables') or 0} tables · {e.get('parse_ms') or '-'} ms")
        if e["md"] and e.get("md_bytes", 0) > 300_000:
            body = (f'<p class=warn>rendered markdown is {e["md_bytes"]/1e6:.1f} MB — open the '
                    f'file to inspect it (tables with hundreds of thousands of cells).</p>')
            extra = f'<a href="{e["md"]}">.md</a> · <a href="{e["json"]}">.json</a>'
        elif e["md"]:
            md = (out / e["md"]).read_text(encoding="utf-8", errors="replace")
            body = render_md(md)
            extra = (f'<details><summary>raw markdown</summary><pre class=raw>{html.escape(md)}</pre></details>'
                     f' · <a href="{e["md"]}">.md</a> · <a href="{e["json"]}">.json</a>')
        else:
            body = f'<p class=err>not produced: {html.escape(str(e.get("error")))}</p>'
            extra = ""
        rows.append(f"""<section>
  <h2><a href="{html.escape(orig)}">{html.escape(e['name'])}</a>
      <small>{e['status']}</small></h2>
  <div class=meta>{html.escape(meta)}</div>
  {body}
  <div class=extra>{extra}</div>
</section>""")
    (out / "index.html").write_text(f"""<!doctype html><meta charset=utf-8>
<title>ParseDock comparison</title>
<style>
 body{{font:15px/1.55 -apple-system,Segoe UI,Roboto,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem;color:#111}}
 section{{border-top:1px solid #ddd;padding:1rem 0 2rem}}
 h2{{font-size:1.05rem;margin:.2rem 0 .3rem}} h2 small{{font-weight:400;color:#777;margin-left:.5rem}}
 .meta{{color:#666;font-size:.82rem;margin-bottom:.8rem}}
 table{{border-collapse:collapse;margin:.6rem 0}} th,td{{border:1px solid #ccc;padding:.3rem .5rem;text-align:left}}
 th{{background:#f4f4f4}} pre{{background:#f7f7f7;padding:.6rem;overflow:auto;white-space:pre-wrap}}
 pre.raw{{font-size:.8rem;max-height:24rem}} .err{{color:#a00}}
 .warn{{color:#a60}} @media (prefers-color-scheme:dark){{body{{background:#111;color:#eee}} section{{border-color:#333}}
  th{{background:#222}} pre{{background:#1b1b1b}} .meta{{color:#999}} }}
</style>
<h1>ParseDock comparison</h1>
<p class=meta>{len(entries)} files from <code>{html.escape(str(root))}</code> · click a title to open the
original in your viewer, the markdown is rendered below it.</p>
{''.join(rows)}
""", encoding="utf-8")
    print(f"\nopen: {out/'index.html'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
