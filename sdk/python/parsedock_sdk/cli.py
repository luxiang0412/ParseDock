"""Command line entry point: `parsedock parse ./file.pdf -o out.md`.

Thin wrapper over ParseDockClient so an operator can smoke-test a deployment without
writing code. Exit codes: 0 success, 1 job failure, 2 usage/connection error.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import __version__
from .client import ParseDockClient, ParseDockError


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="parsedock", description="ParseDock client")
    p.add_argument("--version", action="version", version=f"parsedock {__version__}")
    sub = p.add_subparsers(dest="cmd", required=True)

    parse = sub.add_parser("parse", help="parse a document and write one representation")
    parse.add_argument("file", help="document to parse")
    parse.add_argument("-o", "--out", default="-", help="output path ('-' = stdout)")
    parse.add_argument("--format", default="markdown", choices=["markdown", "text", "json"])
    parse.add_argument("--mode", default="auto", choices=["fast", "accurate", "auto"])
    parse.add_argument("--device", default="auto",
                       choices=["auto", "cpu", "gpu_preferred", "gpu_required"])
    parse.add_argument("--api", default="http://127.0.0.1:8080")
    parse.add_argument("--token", default=None, help="API key (default: PARSEDOCK_TOKEN)")
    parse.add_argument("--timeout", type=float, default=300.0)
    parse.add_argument("--keep", action="store_true",
                       help="do not ACK (release) the result after downloading it")
    parse.add_argument("--json-status", action="store_true", help="print the job status as JSON")

    status = sub.add_parser("status", help="show a job")
    status.add_argument("job_id")
    status.add_argument("--api", default="http://127.0.0.1:8080")
    status.add_argument("--token", default=None)
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    import os
    token = args.token or os.environ.get("PARSEDOCK_TOKEN", "")
    try:
        with ParseDockClient(args.api, token=token) as client:
            if args.cmd == "status":
                print(json.dumps(client.get_job(args.job_id), indent=2))
                return 0

            job = client.create_job(args.file, mode=args.mode, device=args.device)
            job_id = job["job_id"]
            final = client.wait(job_id, timeout=args.timeout)
            if args.json_status:
                print(json.dumps(final, indent=2), file=sys.stderr)
            # The job view carries both `status` and `processing_status`; accept either so
            # the CLI keeps working if one of them is ever dropped from the schema.
            status = final.get("processing_status") or final.get("status")
            if status != "completed":
                print(f"job {job_id} ended {status}: "
                      f"{final.get('error_code')} {final.get('error_message')}", file=sys.stderr)
                return 1

            data, version = client.download(job_id, fmt=args.format)
            if args.out == "-":
                sys.stdout.buffer.write(data)
                sys.stdout.flush()
            else:
                Path(args.out).write_bytes(data)
            if not args.keep:
                # ACK only after the bytes are safely written (the SDK's contract).
                client.ack(job_id, version)
            return 0
    except ParseDockError as e:
        print(f"error: {e.code} ({e.status}): {e.message}", file=sys.stderr)
        return 2
    except OSError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
