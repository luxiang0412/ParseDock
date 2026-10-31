# Java example

Parses one document and writes the result next to it, releasing the server copy
only after the write succeeded.

```bash
export PARSEDOCK_TOKEN=...            # key from deploy/.env (PARSEDOCK_API_KEYS)
make run                              # bundled fixture → …/pdf_text_en_3p.pdf.md
make run FILE=~/docs/report.pdf MODE=accurate
```

Or without make (the client resolves from Maven Central once 0.1.0 is published;
otherwise install it locally first):

```bash
mvn -f ../../sdk/java-generated/pom.xml -DskipTests install   # local fallback
PARSEDOCK_TOKEN=... mvn -q compile exec:java \
  -Dexec.args="$HOME/docs/report.pdf"
```

Environment: `PARSEDOCK_API` (default `http://127.0.0.1:8080`), `PARSEDOCK_MODE`
(`auto|fast|accurate`), `PARSEDOCK_FORMAT` (`markdown|text|json`),
`PARSEDOCK_KEEP=1` (do not ACK). Exit codes: `0` ok, `1` server/parse error,
`2` configuration error.

The client has **no retry policy of its own**: retry `429 rate_limited` and 5xx
with backoff, never the other 4xx codes.
