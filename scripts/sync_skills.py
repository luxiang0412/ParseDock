#!/usr/bin/env python3
"""Keep the agent skills in .agents/skills/ portable and in sync with the docs.

Each skill directory must work when copied into another repository: SKILL.md may only
link into its own references/ folder, and everything it links to lives there. This
script writes those reference files from the documents that own the facts:

    references/error-codes.md   <- docs/ERROR-CODES.md (code table, without "Origin")
    references/document-ir.md   <- docs/DOCUMENT-IR.md §2 and §2a
    references/openapi.yaml     <- api/openapi.yaml (verbatim)

Text between <!-- skill:omit --> and <!-- /skill:omit --> in a source document is
repository-internal (test names, internal constants) and is left out.

It also checks every SKILL.md: the "Applies to ParseDock API `X.Y`" line must match
api/openapi.yaml, and no line may point at repository paths (docs/, internal/, sdk/,
make targets, ...) that do not exist outside this repository.

    python3 scripts/sync_skills.py          # regenerate
    python3 scripts/sync_skills.py --check  # fail on drift or a non-portable reference (CI)
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SKILLS = ROOT / ".agents" / "skills"

OMIT = re.compile(r"<!-- skill:omit -->.*?<!-- /skill:omit -->\n?", re.S)
# What a portable skill must not mention: paths and commands of this repository.
NOT_PORTABLE = re.compile(
    r"(?<![\w/.-])(docs|internal|tests|scripts|sdk|web|examples|benchmark|deploy|cmd)/[\w./-]+"
    r"|`make [\w-]+`|\bmake (?:run|sdk|test|build)\b|mvn -f |pip install -e "
    r"|\]\((?!https?://|#|references/)[^)\s]*[/.][^)]*\)")


def api_version() -> str:
    m = re.search(r"^info:\n(?:  .*\n)*?  version: (\S+)", (ROOT / "api/openapi.yaml").read_text(), re.M)
    if not m:
        sys.exit("sync_skills: no info.version in api/openapi.yaml")
    return m.group(1)


def header(src: str, version: str) -> str:
    return (f"<!-- Generated from ParseDock {src} (API {version}) by scripts/sync_skills.py. "
            f"Do not edit here. -->\n\n")


def section(text: str, start: str, end: str) -> str:
    """Lines from the heading `start` up to (not including) the heading `end`."""
    i = text.index(start)
    j = text.index(end, i)
    return text[i:j].rstrip() + "\n"


def error_codes(version: str) -> str:
    src = OMIT.sub("", (ROOT / "docs/ERROR-CODES.md").read_text())
    envelope = src[src.index("Every error"): src.index("The set of codes")].rstrip()
    rows = []
    codes = src[src.index("## Codes"):]
    nxt = codes.find("\n## ", 1)
    for line in (codes if nxt < 0 else codes[:nxt]).splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        rows.append("| " + " | ".join(cells[:4]) + " |")  # drop "Origin": repository-internal
    if len(rows) < 3:
        sys.exit("sync_skills: no code table found in docs/ERROR-CODES.md")
    return (header("docs/ERROR-CODES.md", version) + "# Error codes\n\n" + envelope + "\n\n"
            "* **HTTP**: the status the code comes with as a response. \"job\" means it is (also)\n"
            "  the `error_code` of a failed job (job resource, event stream, and `422` from\n"
            "  `POST /v1/parse`).\n"
            "* **Client retry**: whether retrying the same request can succeed (the Python SDK's\n"
            "  `ParseDockError.retryable`). Honour `Retry-After` when present. A job's own\n"
            "  `error_code` is final: the server already re-ran retryable attempts.\n\n"
            + "\n".join(rows) + "\n")


def document_ir(version: str) -> str:
    src = OMIT.sub("", (ROOT / "docs/DOCUMENT-IR.md").read_text())
    body = section(src, "## 2. Structure", "## 3. ")
    return header("docs/DOCUMENT-IR.md", version) + "# Document IR `doc-ir/0.1`\n\n" + body


def openapi(version: str) -> str:
    return (f"# Generated from ParseDock api/openapi.yaml (API {version}) by scripts/sync_skills.py. "
            "Do not edit here.\n" + (ROOT / "api/openapi.yaml").read_text())


def check_portable(path: Path, text: str) -> list[str]:
    bad = []
    for n, line in enumerate(text.splitlines(), 1):
        if line.startswith("<!-- Generated from") or line.startswith("# Generated from"):
            continue
        for m in NOT_PORTABLE.finditer(line):
            bad.append(f"{path.relative_to(ROOT)}:{n}: not portable: {m.group(0)}")
    return bad


def main() -> int:
    check = "--check" in sys.argv[1:]
    version = api_version()
    minor = ".".join(version.split(".")[:2])
    generated = {"error-codes.md": error_codes(version), "document-ir.md": document_ir(version),
                 "openapi.yaml": openapi(version)}
    problems: list[str] = []
    skills = sorted(p for p in SKILLS.iterdir() if (p / "SKILL.md").is_file())
    if not skills:
        sys.exit("sync_skills: no skills found")
    for skill in skills:
        md = (skill / "SKILL.md").read_text()
        if f"Applies to ParseDock API `{minor}`" not in md:
            problems.append(f"{skill.name}/SKILL.md: missing 'Applies to ParseDock API `{minor}`'")
        problems += check_portable(skill / "SKILL.md", md)
        for link in re.findall(r"\]\((references/[^)#]+)", md):
            if link.split("/", 1)[1] not in generated:
                problems.append(f"{skill.name}/SKILL.md: links to {link}, which is not generated")
        ref = skill / "references"
        for name, text in generated.items():
            if name.endswith(".md"):
                problems += check_portable(ref / name, text)
            target = ref / name
            if check:
                if not target.is_file() or target.read_text() != text:
                    problems.append(f"{target.relative_to(ROOT)} is stale: run python3 scripts/sync_skills.py")
            else:
                ref.mkdir(exist_ok=True)
                target.write_text(text)
    for p in problems:
        print(p, file=sys.stderr)
    if problems:
        return 1
    print(f"skills in sync with API {version}: {', '.join(s.name for s in skills)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
