#!/usr/bin/env python3
"""Generate docs/repository-tree.md from the repository itself.

The LLD Appendix D requires the actual repository tree and the supported
adapter versions to be provided. A generated document cannot drift from the
code the way a hand-written list does, so this script reads the working tree
and the adapter fixtures and writes the document.

Usage:
    python3 scripts/generate-repository-tree.py            # write the document
    python3 scripts/generate-repository-tree.py --check    # fail if stale
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
OUTPUT = REPO_ROOT / "docs" / "repository-tree.md"

# Directories that never carry source worth listing.
SKIP_DIRS = {
    ".git",
    ".github",
    ".ivp",
    ".tooling",
    "__pycache__",
    "node_modules",
    "dist",
}

# Files whose content is generated or vendored.
SKIP_FILES = {
    ".gitignore",
    ".dockerignore",
    "go.sum",
    "historical-replay.exe",
}

# Files that are listed by name only (their content is not interesting for a
# tree document, and some of them are large).
SKIP_CONTENT = {
    "docs/requirements-inventory.json",
    "docs/Incremental_Verification_Planner_LLD.md",
    "docs/implementation-tracker.md",
    "docs/implementation-log.md",
    # The generated document itself is excluded so that regenerating it is
    # idempotent.
    "docs/repository-tree.md",
}


def repo_files() -> list[Path]:
    """Return every tracked-worthy file in the repository, sorted."""
    files: list[Path] = []
    for path in REPO_ROOT.rglob("*"):
        if not path.is_file():
            continue
        rel = path.relative_to(REPO_ROOT)
        if rel.parts[0] in SKIP_DIRS:
            continue
        if any(part in SKIP_DIRS for part in rel.parts):
            continue
        if rel.name in SKIP_FILES:
            continue
        if rel.as_posix() in SKIP_CONTENT:
            continue
        files.append(rel)
    return sorted(files, key=lambda p: p.as_posix())


def render_tree(files: list[Path]) -> str:
    """Render the repository files as an indented tree."""
    tree: dict = {}
    for rel in files:
        node = tree
        for part in rel.parts[:-1]:
            node = node.setdefault(part, {})
        node.setdefault(rel.parts[-1], None)

    lines: list[str] = []

    def walk(node: dict, prefix: str) -> None:
        entries = sorted(node.items(), key=lambda item: (item[1] is not None, item[0].lower()))
        for index, (name, child) in enumerate(entries):
            last = index == len(entries) - 1
            connector = "└── " if last else "├── "
            if child is None:
                lines.append(f"{prefix}{connector}{name}")
            else:
                lines.append(f"{prefix}{connector}{name}/")
                walk(child, prefix + ("    " if last else "│   "))

    walk(tree, "")
    return "\n".join(lines)


def adapter_versions() -> list[tuple[str, list[str], str, str]]:
    """Read the supported adapter tool versions and capabilities.

    The version directories under internal/adapters/fixtures are the golden
    corpus the shared contract test drives, so they are the authoritative
    record of which tool versions the repository supports. The selection
    granularity is read from the adapter capability declarations. Only
    directories that actually declare adapter capabilities are adapters; the
    shared base packages (native) and the fixture corpus are not.
    """
    fixtures = REPO_ROOT / "internal" / "adapters" / "fixtures"
    sources = REPO_ROOT / "internal" / "adapters"
    rows: list[tuple[str, list[str], str, str]] = []
    for adapter_dir in sorted(p for p in sources.iterdir() if p.is_dir()):
        name = adapter_dir.name
        granularity = ""
        formats = ""
        for go_file in sorted(adapter_dir.glob("*.go")):
            if go_file.name.endswith("_test.go"):
                continue
            text = go_file.read_text(encoding="utf-8")
            if not granularity:
                match = re.search(r'SelectableGranularity:\s*"([A-Z_]+)"', text)
                if match:
                    granularity = match.group(1)
            if not formats:
                match = re.search(r"ResultFormats:\s*\[\]string\{([^}]*)\}", text)
                if match:
                    formats = ", ".join(re.findall(r'"([^"]+)"', match.group(1)))
        if not granularity and not formats:
            # Not an adapter package (shared base code or the fixture corpus).
            continue
        versions: list[str] = []
        fixture_dir = fixtures / name
        if fixture_dir.is_dir():
            versions = sorted(p.name for p in fixture_dir.iterdir() if p.is_dir())
        rows.append((name, versions, granularity, formats))
    return rows


def render_document() -> str:
    files = repo_files()
    tree = render_tree(files)
    rows = adapter_versions()

    lines: list[str] = []
    lines.append("# Repository tree and supported adapter versions")
    lines.append("")
    lines.append(
        "This document is generated from the repository by "
        "`scripts/generate-repository-tree.py`. Do not edit it by hand; run the "
        "script and commit the result."
    )
    lines.append("")
    lines.append("## Repository tree")
    lines.append("")
    lines.append("```")
    lines.append(tree)
    lines.append("```")
    lines.append("")
    lines.append(f"The tree lists {len(files)} source-controlled files.")
    lines.append("")
    lines.append("## Supported adapter versions")
    lines.append("")
    lines.append(
        "The tool versions are the golden-fixture corpus directories the shared "
        "equivalent-outcome contract test drives "
        "(`internal/adapters/equivalent_outcome_test.go`); every listed version "
        "is exercised for every report category. The selection granularity is "
        "the adapter's declared `Capabilities.SelectableGranularity`, and the "
        "report formats are its declared `Capabilities.ResultFormats`."
    )
    lines.append("")
    lines.append("| Adapter | Supported tool versions | Selection granularity | Report formats |")
    lines.append("|---|---|---|---|")
    for name, versions, granularity, formats in rows:
        version_text = ", ".join(f"`{v}`" for v in versions) if versions else "(none)"
        lines.append(f"| {name} | {version_text} | `{granularity or 'n/a'}` | {formats or 'n/a'} |")
    lines.append("")
    lines.append("## Layout")
    lines.append("")
    lines.append("| Path | Purpose |")
    lines.append("|---|---|")
    lines.append("| `cmd/ivp/` | CLI entry point (`ivp`): plan, run, audit, report, index. |")
    lines.append("| `cmd/ivp-api/` | API and maintenance entry point (`ivp-api`): HTTP server, migrations, retention. |")
    lines.append("| `api/` | Hand-written OpenAPI 3.1 description of the HTTP surface. |")
    lines.append("| `internal/analysis/` | Planner, impact analysis, derived graph index, risk scoring. |")
    lines.append("| `internal/adapters/` | The seven build-system adapters and their golden fixture corpus. |")
    lines.append("| `internal/api/` | HTTP routing, request validation, idempotency, error mapping. |")
    lines.append("| `internal/artifact/` | Bounded tenant-scoped artifact stores (filesystem and S3). |")
    lines.append("| `internal/audit/` | Audit comparison and quarantine decisions. |")
    lines.append("| `internal/cli/` | CLI command implementations. |")
    lines.append("| `internal/config/` | Configuration schema, defaults, precedence, and validation. |")
    lines.append("| `internal/domain/` | Shared domain types, enums, and validation. |")
    lines.append("| `internal/execution/` | Bounded process runner and scheduler. |")
    lines.append("| `internal/persistence/` | PostgreSQL schema, migrations, and repositories. |")
    lines.append("| `internal/security/` | Path safety, redaction, principals, policy signatures. |")
    lines.append("| `internal/telemetry/` | Prometheus metrics and OpenTelemetry tracing. |")
    lines.append("| `deploy/helm/ivp/` | Kubernetes chart: API, migration job, retention CronJob, PDB, NetworkPolicy. |")
    lines.append("| `deploy/observability/` | Prometheus alert rules, scrape config, collector and dashboard. |")
    lines.append("| `docs/` | Architecture, operations, security, threat model, ADRs, and generated documents. |")
    lines.append("| `scripts/` | Operational verification and generation scripts. |")
    lines.append("| `schemas/` | JSON schema for the repository configuration file. |")
    lines.append("| `tests/` | End-to-end, performance, and historical-replay suites. |")
    lines.append("")
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit non-zero when the checked-in document is stale",
    )
    args = parser.parse_args()

    document = render_document()
    if args.check:
        if not OUTPUT.exists():
            print(f"{OUTPUT.relative_to(REPO_ROOT)} is missing", file=sys.stderr)
            return 1
        if OUTPUT.read_text(encoding="utf-8") != document:
            print(
                f"{OUTPUT.relative_to(REPO_ROOT)} is stale; run scripts/generate-repository-tree.py",
                file=sys.stderr,
            )
            return 1
        print(f"{OUTPUT.relative_to(REPO_ROOT)} is up to date")
        return 0

    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    OUTPUT.write_text(document, encoding="utf-8", newline="\n")
    print(f"wrote {OUTPUT.relative_to(REPO_ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
