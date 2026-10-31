# Single-host deployment (4 containers)

Coordinator, one API replica, one CPU worker, and the Playground — on one
Docker host, with **two published ports**:

| Port | Service | Notes |
| --- | --- | --- |
| `8080` (`API_PORT`) | public API | Bearer key required; `mode`, upload, poll, download, ACK |
| `8090` (`PLAYGROUND_PORT`) | Playground UI | Static app + same-origin `/v1` proxy that injects the key |

The coordinator (`:8081`) and the worker protocol are **not** published: they sit
on the `backend` network, which is `internal: true`.

## 1. Prerequisites

- Docker Engine 24+ with Compose v2 (verified on 27.1.2 / v2.29.1)
- x86-64 Linux; ~2 GiB RAM for the stack (worker parses with 2 slots)
- ~2 GiB of disk for images, plus the data volume:
  `2.5 × daily ingress bytes × TTL days` (see `docs/DEPLOYMENT.md` §6a)
- The three images loaded on the host:

```bash
# from a machine that has them
docker save parsedock:0.1.0 parsedock-worker-cpu:0.1.0 parsedock-playground:0.1.0 \
  | gzip -1 | ssh HOST 'gunzip | docker load'      # ~1.3 GiB, 2 min on 1 GbE
```

The CPU worker image bundles the xberg and docling-rs engines and their models;
it must not be redistributed while licence item **L1** (`libx265`, GPL-2.0) is
open (`docs/LICENSE-REVIEW.md`).

## 2. Configure

```bash
cp .env.example .env && chmod 600 .env
openssl rand -hex 24   # generate three of these
```

- `PARSEDOCK_INTERNAL_TOKEN` — coordinator ↔ api/worker
- `PARSEDOCK_API_KEYS` — `<token>:<owner>` pairs, comma-separated; the same
  Playground token must appear here as `<token>:playground`
- `PLAYGROUND_KEY` — a bare token, injected into proxied requests

`PARSEDOCK_XBERG_OCR_LANG` selects the tesseract language for **scanned** pages
(`zh` for Chinese, `eng` for Latin). A wrong value does not fail the job — it
silently degrades OCR text, so set it to match the corpus.

## 3. Run

```bash
docker compose up -d
docker compose ps                       # 4 services, all running
```

## 4. Verify

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://HOST:8080/healthz            # 200
curl -s -o /dev/null -w '%{http_code}\n' http://HOST:8080/readyz             # 200
curl -s -o /dev/null -w '%{http_code}\n' http://HOST:8090/                   # 200
curl -s -o /dev/null -w '%{http_code}\n' http://HOST:8090/internal/metrics   # 404
curl -s -o /dev/null -w '%{http_code}\n' http://HOST:8080/v1/jobs/x          # 401
KEY=<token from PARSEDOCK_API_KEYS>
curl -s -H "Authorization: Bearer $KEY" -F file=@sample.pdf \
  http://HOST:8080/v1/jobs | tee /tmp/job.json     # 202 + job_id
ID=$(python3 -c 'import json;print(json.load(open("/tmp/job.json"))["job_id"])')
curl -s -H "Authorization: Bearer $KEY" http://HOST:8080/v1/jobs/$ID          # completed
curl -s -H "Authorization: Bearer $KEY" "http://HOST:8080/v1/jobs/$ID/result?format=markdown"
# ACK deletes the server copy (this is a delete request, not an acknowledgement):
curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"result_version\":\"$(curl -sI -H "Authorization: Bearer $KEY" "http://HOST:8080/v1/jobs/$ID/result" | tr -d '\r' | awk -F': ' 'tolower($1)=="x-result-version"{print $2}')\"}" \
  http://HOST:8080/v1/jobs/$ID/ack
```

The Playground needs no key in the browser; the proxy adds it. Its
`POST /v1/jobs` requires the `X-Parsedock-Playground: 1` header (CSRF), which the
app sends itself — a bare `curl -F` against `:8090` therefore returns 403.

## 5. Operate

```bash
docker compose logs -f api coordinator worker-cpu    # no document text is logged
docker compose restart api                          # stateless, safe
docker compose down                                 # volumes are kept
docker compose pull && docker compose up -d         # upgrade: load new images first
```

- **Back up** with `scripts/backup_online.py` (lease-pinned, verified); a raw
  `cp` of a live SQLite file is not a backup. See `docs/RUNBOOK.md`.
- **Scale out**: add `api` replicas (stateless) behind any load balancer; add
  `worker-cpu` replicas for throughput. The coordinator must stay a single
  instance (SQLite single writer).
- **Do not** put the data volume on NFS/SMB — SQLite WAL requires local disk.
- `PARSEDOCK_RESULT_TTL` (default `1h`) governs how long an un-ACKed result
  lives; the API caps any request at 24 h.
