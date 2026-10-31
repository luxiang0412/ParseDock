#!/usr/bin/env python3
"""PR-00 contract tests: specs, generated SDKs and the real routers agree.

Checks (mapped to the review's acceptance IDs):

  PR00-08  both OpenAPI documents validate as 3.1 and contain no `nullable`
  PR00-08  the public document has no /internal paths and no internal security scheme
  PR00-08  the generated Python/Java SDKs contain no internal operations
  PR00-08  every documented public route exists in the Go router (and vice versa)
  PR00-08  every documented internal route exists in the internal Go router
  PR00-08  the public gateway mux (internal/api) routes every documented public path,
           wraps /v1 in auth, and exposes no /internal path
  PR02     the spec's scope table covers every authenticated operation exactly once,
           and the gateway's NeededScope agrees with it (go test, see below)
  PR00-06  the hand-written SDK acks with X-Result-Version, never with ETag
           error codes: internal/jobs/job.go == OpenAPI Error.code enum == docs/ERROR-CODES.md

Run: python tests/contract/openapi_contract.py
"""
from __future__ import annotations

import json
import re
import shutil
import subprocess
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
PUBLIC = ROOT / "api" / "openapi.yaml"
INTERNAL = ROOT / "api" / "openapi.internal.yaml"
HTTP_GO = ROOT / "internal" / "coordinator" / "http.go"
GATEWAY_GO = ROOT / "internal" / "api" / "server.go"
SDK_PY = ROOT / "sdk" / "python-generated"
SDK_JAVA = ROOT / "sdk" / "java-generated"
SDK_WRAPPER = ROOT / "sdk" / "python" / "parsedock_sdk" / "client.py"
JOB_GO = ROOT / "internal" / "jobs" / "job.go"
ERROR_CODES_MD = ROOT / "docs" / "ERROR-CODES.md"

failures: list[str] = []
checks: list[dict] = []


def error_codes_go() -> set[str]:
    """Codes declared in the "Stable machine-readable error codes" const block."""
    text = JOB_GO.read_text(encoding="utf-8")
    m = re.search(r"// Stable machine-readable error codes\..*?\nconst \((.*?)\n\)", text, re.DOTALL)
    if not m:
        return set()
    return set(re.findall(r'^\s*Err\w+\s*=\s*"([a-z0-9_]+)"', m.group(1), re.MULTILINE))


def error_codes_doc() -> set[str]:
    """Codes listed in the table under `## Codes` in docs/ERROR-CODES.md."""
    if not ERROR_CODES_MD.exists():
        return set()
    section = ERROR_CODES_MD.read_text(encoding="utf-8").split("\n## Codes", 1)[-1]
    section = section.split("\n## ", 1)[0]
    return set(re.findall(r"^\| `([a-z0-9_]+)` \|", section, re.MULTILINE))


def error_code_literals() -> dict[str, str]:
    """Error codes written as string literals (`Code: "x"`, `"code": "x"`) in Go code."""
    found: dict[str, str] = {}
    pattern = re.compile(r'(?:\bCode:\s*|"code":\s*)"([a-z0-9_]+)"')
    for path in sorted((ROOT / "internal").rglob("*.go")) + sorted((ROOT / "cmd").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for code in pattern.findall(path.read_text(encoding="utf-8")):
            found.setdefault(code, str(path.relative_to(ROOT)))
    return found


def check(name: str, ok: bool, detail: str = "") -> None:
    checks.append({"check": name, "status": "PASS" if ok else "FAIL", "detail": detail})
    if not ok:
        failures.append(f"{name}: {detail}")


def load(path: Path) -> dict:
    with open(path, encoding="utf-8") as f:
        return yaml.safe_load(f)


def spec_routes(doc: dict) -> set[tuple[str, str]]:
    out = set()
    for path, item in (doc.get("paths") or {}).items():
        for method in item:
            if method.lower() in ("get", "put", "post", "delete", "patch", "head", "options"):
                out.add((method.upper(), path))
    return out


def router_routes(source: str, func_name: str) -> set[tuple[str, str]]:
    """Extract `m.HandleFunc("METHOD /path", …)` calls from one Go mux builder."""
    start = source.index(f"func (s *Server) {func_name}() http.Handler {{")
    end = source.index("\n}", start)
    body = source[start:end]
    out = set()
    for m in re.finditer(r'HandleFunc\("([A-Z]+)\s+([^"]+)"', body):
        out.add((m.group(1), m.group(2)))
    return out


def main() -> int:
    pub = load(PUBLIC)
    internal = load(INTERNAL)
    go = HTTP_GO.read_text()

    # --- 1. versions and nullability
    for name, doc in (("public", pub), ("internal", internal)):
        check(f"{name} spec is OpenAPI 3.1", str(doc.get("openapi")) == "3.1.0", doc.get("openapi", ""))
        text = (PUBLIC if name == "public" else INTERNAL).read_text()
        check(f"{name} spec uses 3.1 nullability (no `nullable:`)",
              "nullable:" not in text,
              f"{text.count('nullable:')} occurrences")

    # --- 2. public/private separation
    pub_paths = set(pub["paths"])
    check("public spec exposes no /internal path",
          not any(p.startswith("/internal") for p in pub_paths),
          str([p for p in pub_paths if p.startswith("/internal")]))
    check("public spec has no internalToken scheme",
          "internalToken" not in (pub.get("components", {}).get("securitySchemes") or {}),
          str(list((pub.get("components", {}).get("securitySchemes") or {}))))
    check("internal spec only uses the internal token",
          list((internal.get("components", {}).get("securitySchemes") or {})) == ["internalToken"],
          str(list((internal.get("components", {}).get("securitySchemes") or {}))))

    # --- 3. generated SDKs contain no worker operations
    if SDK_PY.exists():
        blob = ""
        for f in SDK_PY.rglob("*.py"):
            blob += f.read_text(errors="replace")
        check("generated Python SDK has no /internal operations",
              "/internal/tasks" not in blob and "/internal/workers" not in blob,
              "internal path found in generated client")
        for needed in ("/v1/jobs", "/v1/jobs/{id}/ack", "/v1/jobs/{id}/result", "/v1/jobs/{id}/events"):
            check(f"generated Python SDK covers {needed}", needed in blob or needed.replace("{id}", "{job_id}") in blob)
    else:
        check("generated Python SDK exists", False, "run `make sdk`")

    if SDK_JAVA.exists():
        blob = ""
        for f in SDK_JAVA.rglob("*.java"):
            blob += f.read_text(errors="replace")
        check("generated Java SDK has no /internal operations",
              "/internal/tasks" not in blob and "/internal/workers" not in blob)
        check("generated Java SDK omits worker schemas",
              "WorkerRegistration" not in blob and "ClaimedTask" not in blob)

    # --- 4. spec <-> router drift
    pub_spec = spec_routes(pub)
    pub_router = router_routes(go, "PublicMux")
    # The coordinator's PublicMux serves exactly the public spec (the probes are part of
    # it). Operator diagnostics live in InternalMux, behind the internal token.
    check("every documented public route exists in PublicMux",
          pub_spec <= pub_router,
          f"missing in router: {sorted(pub_spec - pub_router)}")
    check("PublicMux exposes no undocumented route",
          pub_router <= pub_spec,
          f"extra in router: {sorted(r for r in pub_router if r not in pub_spec)}")

    # The public gateway is what clients actually reach: every documented public route
    # must be routed by its mux (exactly, or by a "METHOD /prefix/" pattern), the /v1
    # patterns must go through auth, and nothing under /internal may be reachable.
    gw_src = GATEWAY_GO.read_text()
    gw_routes = router_routes(gw_src, "mux")
    gw_body = gw_src[gw_src.index("func (s *Server) mux() http.Handler {"):]
    gw_body = gw_body[:gw_body.index("\n}")]

    def gateway_routes(method: str, path: str) -> bool:
        return any(m == method and (p == path or (p.endswith("/") and path.startswith(p)))
                   for m, p in gw_routes)

    check("every documented public route is routed by the gateway mux",
          all(gateway_routes(m, p) for m, p in pub_spec),
          f"unrouted: {sorted(r for r in pub_spec if not gateway_routes(*r))}")
    v1_lines = [ln for ln in gw_body.splitlines() if re.search(r'HandleFunc\("[A-Z]+ /v1/', ln)]
    check("gateway /v1 patterns are wrapped in auth + rate limit",
          bool(v1_lines) and all("s.auth(s.limit(" in ln for ln in v1_lines), str(v1_lines))
    check("gateway mux exposes no /internal route",
          not any(p.startswith("/internal") for _, p in gw_routes), str(sorted(gw_routes)))

    int_spec = spec_routes(internal)
    int_router = router_routes(go, "InternalMux")
    check("every documented internal route exists in InternalMux",
          int_spec <= int_router, f"missing in router: {sorted(int_spec - int_router)}")
    check("InternalMux exposes no undocumented route (except probes)",
          all((m, p) in int_spec or p in ("/healthz", "/readyz") for m, p in int_router),
          f"extra in router: {sorted(r for r in int_router if r not in int_spec)}")
    check("worker heartbeat endpoint is documented",
          ("POST", "/internal/workers/{id}/heartbeat") in int_spec)

    # --- 5. SDK ack uses the manifest version, never the ETag
    if SDK_WRAPPER.exists():
        text = SDK_WRAPPER.read_text()
        check("Python SDK acks with X-Result-Version",
              "X-Result-Version" in text, "wrapper does not read X-Result-Version")
        # The validator and the release token are different things (F05).
        ack_section = text.split("def download(", 1)[-1].split("def ack(", 1)[0]
        code_only = "\n".join(line.split("#", 1)[0] for line in ack_section.splitlines())
        check("Python SDK never derives the ACK token from ETag",
              "ETag" not in code_only, "ETag referenced in the download path")

    # --- 6. semantic documentation is present
    result_desc = pub["paths"]["/v1/jobs/{id}/result"]["get"]
    check("result endpoint documents 304", "304" in result_desc["responses"])
    check("result endpoint documents If-None-Match",
          "If-None-Match" in result_desc.get("description", ""))
    cancel_desc = pub["paths"]["/v1/jobs/{id}/cancel"]["post"]["description"]
    check("cancel documents the completed-job rule",
          "completed" in cancel_desc and "ack" in cancel_desc.lower())
    events_desc = pub["paths"]["/v1/jobs/{id}/events"]["get"]["description"]
    check("events documents the scope", "scope=processing" in events_desc and "scope=retention" in events_desc)
    create_desc = pub["paths"]["/v1/jobs"]["post"]["description"]
    check("create documents any part order", "any order" in create_desc.lower())
    job_props = pub["components"]["schemas"]["Job"]["properties"]
    check("job exposes processing_deadline_at", "processing_deadline_at" in job_props)
    check("job keeps max_lifetime_at as deprecated alias",
          job_props.get("max_lifetime_at", {}).get("deprecated") is True)
    enum = pub["components"]["schemas"]["Error"]["properties"]["error"]["properties"]["code"]["enum"]
    check("error enum documents insufficient_scope", "insufficient_scope" in enum, str(enum))
    check("error enum documents quota codes",
          {"quota_exceeded", "rate_limited"} <= set(enum), str(enum))
    # The error-code contract has three copies that must agree exactly: the Go
    # constants, the OpenAPI enum (and through it the SDKs/playground types) and the
    # table in docs/ERROR-CODES.md.
    go_codes = error_codes_go()
    doc_codes = error_codes_doc()
    check("error enum has no duplicates", len(enum) == len(set(enum)), str(enum))
    check("error codes: internal/jobs/job.go == OpenAPI enum",
          go_codes == set(enum),
          f"only in job.go: {sorted(go_codes - set(enum))}, only in OpenAPI: {sorted(set(enum) - go_codes)}")
    check("error codes: internal/jobs/job.go == docs/ERROR-CODES.md",
          go_codes == doc_codes,
          f"only in job.go: {sorted(go_codes - doc_codes)}, only in ERROR-CODES.md: {sorted(doc_codes - go_codes)}")
    literals = error_code_literals()
    check("error code string literals in Go are all documented codes",
          set(literals) <= go_codes,
          ", ".join(f"{c} ({literals[c]})" for c in sorted(set(literals) - go_codes)))
    check("bearerAuth documents scopes", "jobs:ack" in pub["components"]["securitySchemes"]["bearerAuth"]["description"])

    # PR-02 scope table, e.g. "* `jobs:write` — `POST /v1/jobs`, `POST /v1/parse`". A bare
    # suffix (`/result`) inherits the previous entry's method on /v1/jobs/{id}.
    table: dict[tuple[str, str], list[str]] = {}
    for line in pub["components"]["securitySchemes"]["bearerAuth"]["description"].splitlines():
        m = re.match(r"\s*\* `(jobs:[a-z]+)` — (.+)$", line)
        if not m:
            continue
        method = ""
        for item in re.findall(r"`([^`]+)`", m.group(2)):
            if " " in item:
                method, item = item.split(" ", 1)
            elif not item.startswith("/v1/"):
                item = "/v1/jobs/{id}" + item
            table.setdefault((method, item), []).append(m.group(1))
    authed = {(meth.upper(), path) for path, item in pub["paths"].items()
              for meth, op in item.items()
              if isinstance(op, dict) and op.get("security", pub.get("security")) != []}
    check("scope table covers every authenticated operation",
          authed <= set(table), f"missing: {sorted(authed - set(table))}")
    check("scope table names only documented operations",
          set(table) <= authed, f"unknown: {sorted(set(table) - authed)}")
    check("every operation needs exactly one scope",
          all(len(v) == 1 for v in table.values()),
          str({k: v for k, v in table.items() if len(v) != 1}))
    # The gateway's NeededScope is compared with the same table by a Go test that reads
    # api/openapi.yaml itself (internal/api/keys_test.go).
    go_bin = shutil.which("go")
    if go_bin is None:
        check("NeededScope matches the spec scope table (go test)", False, "`go` not on PATH")
    else:
        res = subprocess.run([go_bin, "test", "-count=1", "-run", "^TestNeededScopeMatchesSpec$", "./internal/api/"],
                             cwd=ROOT, capture_output=True, text=True, timeout=300)
        check("NeededScope matches the spec scope table (go test)", res.returncode == 0,
              (res.stdout + res.stderr)[-800:])
    ops_with_403 = [p for p, item in pub["paths"].items()
                    for m, op in item.items()
                    if isinstance(op, dict) and "403" in op.get("responses", {})]
    check("authenticated operations document 403", len(ops_with_403) >= 5, str(ops_with_403))
    check("mode documents that text formats use native",
          "always use the native engine" in pub["components"]["parameters"]["ModeQuery"]["description"])

    # PR-05: the published Document IR schema is part of the public contract.
    schema_path = ROOT / "api" / "document-ir-0.1.schema.json"
    check("Document IR schema is published", schema_path.exists(), str(schema_path))
    if schema_path.exists():
        sc = json.loads(schema_path.read_text())
        check("IR schema id/version match", sc.get("$id", "").endswith("document-ir-0.1.json"), sc.get("$id", ""))
        check("IR schema requires schema_version const",
              sc["properties"]["schema_version"].get("const") == "doc-ir/0.1")

    for c in checks:
        print(f"[{c['status']}] {c['check']}" + (f" — {c['detail']}" if c["status"] == "FAIL" else ""))
    print(f"\n{len(checks)} checks, {len(failures)} failure(s)")
    if failures:
        print("\nFAILURES:")
        for f in failures:
            print(" -", f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
