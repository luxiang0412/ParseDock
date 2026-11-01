# Python example

Parses one document and writes the result next to it, releasing the server copy
only after the write succeeded.

```bash
export PARSEDOCK_TOKEN=...            # key from deploy/.env (PARSEDOCK_API_KEYS)
make run                              # bundled fixture → pdf_text_en_3p.md
make run FILE=~/docs/report.pdf MODE=accurate
```

Or without make:

```bash
pip install parsedock==0.1.1        # or: pip install -e ../../sdk/python
PARSEDOCK_TOKEN=... python parse.py ~/docs/report.pdf --mode accurate -o report.md
```

`PARSEDOCK_API` (default `http://127.0.0.1:8080`), `--format markdown|text|json`,
`--device auto|cpu|gpu_preferred|gpu_required`, `--keep` (do not ACK).

Exit codes: `0` ok, `1` server/parse error, `2` configuration or connection error.
