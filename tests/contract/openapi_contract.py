#!/usr/bin/env python3
"""PR-00 contract tests: specs, generated SDKs and the real routers agree.

Checks (mapped to the review's acceptance IDs):

  PR00-08  both OpenAPI documents validate as 3.1 and contain no `nullable`
  PR00-08  the public document has no /internal paths and no internal security scheme
  PR00-08  the generated Python/Java SDKs contain no internal operations
  PR00-08  every documented public route exists in the Go router (and vice versa)
  PR00-08  every documented internal route exists in the internal Go router
  PR00-06  the hand-written SDK acks with X-Result-Version, never with ETag

Run: python tests/contract/openapi_contract.py
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
PUBLIC = ROOT / "api" / "openapi.yaml"
INTERNAL = ROOT / "api" / "openapi.internal.yaml"
HTTP_GO = ROOT / "internal" / "coordinator" / "http.go"
SDK_PY = ROOT / "sdk" / "python-generated"
SDK_JAVA = ROOT / "sdk" / "java-generated"
SDK_WRAPPER = ROOT / "sdk" / "python" / "parsedock_sdk" / "client.py"

failures: list[str] = []
checks: list[dict] = []


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
    # diagnostics lives in PublicMux (it is behind the internal token + owner guard)
    check("every documented public route exists in PublicMux",
          pub_spec <= pub_router,
          f"missing in router: {sorted(pub_spec - pub_router)}")
    check("PublicMux exposes no undocumented route (except probes/diagnostics)",
          all((m, p) in pub_spec or p.startswith("/internal/diagnostics")
              for m, p in pub_router),
          f"extra in router: {sorted(r for r in pub_router if r not in pub_spec)}")

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
    check("cancel documents the completed-job rule", "completed" in cancel_desc and "result_version" in cancel_desc)
    events_desc = pub["paths"]["/v1/jobs/{id}/events"]["get"]["description"]
    check("events documents the scope", "scope=processing" in events_desc and "scope=retention" in events_desc)
    create_desc = pub["paths"]["/v1/jobs"]["post"]["description"]
    check("create documents any part order", "any order" in create_desc.lower())
    job_props = pub["components"]["schemas"]["Job"]["properties"]
    check("job exposes processing_deadline_at", "processing_deadline_at" in job_props)
    check("job keeps max_lifetime_at as deprecated alias",
          job_props.get("max_lifetime_at", {}).get("deprecated") is True)
    check("mode documents that text formats use native",
          "always use the native engine" in pub["components"]["parameters"]["ModeQuery"]["description"])

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
