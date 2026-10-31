#!/usr/bin/env python3
"""Minimal ParseDock example: parse one document and store the result.

    PARSEDOCK_TOKEN=... python parse.py invoice.pdf
    PARSEDOCK_TOKEN=... python parse.py scan.pdf --mode accurate --format json -o out.json

The SDK rule this example is written to demonstrate: the server copy is released
(ACKed) only *after* the bytes are safely written. `--keep` skips the ACK so you
can download again while the TTL lasts.
"""
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from parsedock_sdk import ParseDockClient, ParseDockError

DEFAULT_FILE = Path(__file__).resolve().parents[2] / "benchmark" / "fixtures" / "generated" / "pdf_text_en_3p.pdf"


def main() -> int:
    ap = argparse.ArgumentParser(description="Parse a document with ParseDock.")
    ap.add_argument("file", nargs="?", default=str(DEFAULT_FILE))
    ap.add_argument("--api", default=os.environ.get("PARSEDOCK_API", "http://127.0.0.1:8080"))
    ap.add_argument("--token", default=os.environ.get("PARSEDOCK_TOKEN", ""))
    ap.add_argument("--mode", default="auto", choices=["auto", "fast", "accurate"])
    ap.add_argument("--device", default="auto", choices=["auto", "cpu", "gpu_preferred", "gpu_required"])
    ap.add_argument("--format", default="markdown", choices=["markdown", "text", "json"])
    ap.add_argument("-o", "--output", default="")
    ap.add_argument("--keep", action="store_true", help="do not ACK; leave the result on the server")
    args = ap.parse_args()

    if not args.token:
        print("set PARSEDOCK_TOKEN (or pass --token)", file=sys.stderr)
        return 2
    src = Path(args.file)
    if not src.is_file():
        print(f"no such file: {src}", file=sys.stderr)
        return 2
    ext = {"markdown": ".md", "text": ".txt", "json": ".json"}[args.format]
    out = Path(args.output or Path("out") / (src.name + ext))

    try:
        with ParseDockClient(args.api, token=args.token) as c:
            # No idempotency key: each run is a new job. Add one (a stable key of
            # your own) when retrying the same upload must not create a second job
            # - but remember that replaying a key returns the *original* job, whose
            # result may already be released by a previous ACK.
            job = c.create_job(src, mode=args.mode, device=args.device)
            print(f"job {job['job_id']} queued; waiting…")
            final = c.wait(job["job_id"], timeout=600)
            if final["status"] != "completed":
                print(f"parse failed: {final.get('error_code')} {final.get('error_message') or ''}", file=sys.stderr)
                return 1

            body, version = c.download(job["job_id"], fmt=args.format)
            out.parent.mkdir(parents=True, exist_ok=True)
            out.write_bytes(body)                      # store first…
            print(f"{final.get('engine')} · {len(body)} bytes · {version[:19]}… → {out}")

            if args.keep:
                print(f"kept on server; ACK it later with: {version}")
                return 0
            c.ack(job["job_id"], version)              # …release only after the write
            try:
                c.wait_released(job["job_id"], timeout=60)
                print("released (ACK sent after the file was written)")
            except TimeoutError:
                # The release is accepted; deletion is asynchronous. Saying
                # "released" here before it is true would be a lie.
                print("release accepted; deletion still in progress")
            return 0
    except ParseDockError as e:
        print(f"server error {e.status} {e.code}: {e.message}", file=sys.stderr)
        return 1
    except (TimeoutError, OSError) as e:
        print(f"{type(e).__name__}: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
