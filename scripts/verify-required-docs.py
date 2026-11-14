"""Verify that the documents the LLD requires are present and complete.

Usage: python scripts/verify-required-docs.py
"""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

REQUIRED = {
    "docs/threat-model.md": [
        "## Assets",
        "## Trust boundaries",
        "## Threats and mitigations",
        "## Residual risk",
    ],
    "docs/bounds-review.md": [
        "## Graph queries and traversal",
        "## Process spawning",
        "## Output buffering",
        "## Artifact accumulation",
    ],
    "docs/slo-report.md": [
        "API availability",
        "Plan creation p95",
        "indexing p95",
        "## Deviations",
    ],
    "docs/validation-suites.md": [
        "| Suite | Command | Runs on |",
    ],
    "docs/adapter-samples/README.md": [
        "## The shared contract",
    ],
    "docs/repository-tree.md": [],
    "docs/operations.md": [],
    "docs/security.md": [],
    "docs/adapter-support.md": [],
}


def main() -> int:
    missing = []
    incomplete = []
    for relative, sections in REQUIRED.items():
        path = ROOT / relative
        if not path.exists():
            missing.append(relative)
            continue
        content = path.read_text(encoding="utf-8")
        for section in sections:
            if section not in content:
                incomplete.append(f"{relative}: missing section {section!r}")
    if missing or incomplete:
        print("REQUIRED DOCUMENT CHECK FAILED:")
        for relative in missing:
            print(" - missing document:", relative)
        for problem in incomplete:
            print(" -", problem)
        sys.exit(1)
    print(f"required documents OK: {len(REQUIRED)} documents present and complete")
    return 0


if __name__ == "__main__":
    sys.exit(main())
