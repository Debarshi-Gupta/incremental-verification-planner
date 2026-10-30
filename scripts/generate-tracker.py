#!/usr/bin/env python3
"""Build a stable requirement inventory and progress summary from the IVP LLD.

The inventory is deliberately conservative: extracted requirements start as
IN_PROGRESS until an implementation reference and validation evidence have
been reviewed. Existing status/evidence fields are preserved by stable ID.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
LLD = ROOT / "docs" / "Incremental_Verification_Planner_LLD.md"
INVENTORY = ROOT / "docs" / "requirements-inventory.json"
TRACKER = ROOT / "docs" / "implementation-tracker.md"
STATUSES = ("UNREVIEWED", "NOT_STARTED", "IN_PROGRESS", "IMPLEMENTED", "TESTED", "BLOCKED")
NORMATIVE = re.compile(r"\b(?:MUST(?: NOT)?|SHOULD(?: NOT)?|MAY)\b", re.IGNORECASE)
HEADING = re.compile(r"^(?:#{1,6}\s*)?((?:\d+(?:\.\d+)*\.?|Appendix\s+[A-D]\.?)\s+)(.+?)\s*$")
SENTENCE = re.compile(r"(?<=[.!?])\s+(?=(?:[A-Z0-9`]))")
LIST_ITEM = re.compile(r"^\s*(?:[-*+]\s+|\d+[.)]\s+)(.+?)\s*$")
HEADING_TITLES = {
    "0": "Normative implementation instructions",
    "1": "Product definition", "1.1": "Problem", "1.2": "Product outcome",
    "1.3": "User personas", "1.4": "Supported production scope",
    "1.5": "Supported deployment platforms", "1.6": "Explicit non-goals",
    "2": "Design decisions", "2.1": "Architecture style", "2.2": "Technology choices",
    "2.3": "Trust boundaries", "3": "System context and components",
    "3.1": "Logical component diagram", "3.2": "Responsibilities",
    "4": "Core domain model", "4.1": "Repository", "4.2": "Repository snapshot",
    "4.3": "Changed file", "4.4": "Graph nodes and edges", "4.5": "Test unit",
    "4.6": "Coverage and evidence", "4.7": "Verification plan", "4.8": "Execution and audit",
    "5": "Repository configuration", "5.1": "File location and precedence",
    "5.2": "Example complete configuration", "6": "Planner algorithm", "6.1": "Inputs",
    "6.2": "Outputs", "6.3": "Planning stages", "7": "Risk policy and modes",
    "7.1": "Modes", "7.2": "Confidence and calibration", "7.3": "Flaky tests",
    "7.4": "Tests for which no coverage is available", "7.5": "Required gates",
    "8": "Adapter subsystem", "8.1": "Interface contract", "8.2": "Gradle adapter",
    "8.3": "Maven adapter", "8.4": "Bazel adapter", "8.5": "Nx adapter",
    "8.6": "pytest adapter", "8.7": "Jest adapter", "8.8": "Generic adapter",
    "8.9": "Cross-adapter behavior", "9": "Execution engine",
    "9.1": "Local command execution", "9.2": "Execution state machine",
    "9.3": "Plan revalidation", "9.4": "Escalation rules during execution",
    "10": "Audit and calibration engine", "10.1": "Why audit is a required product capability",
    "10.2": "Audit policies", "10.3": "Audit equivalence", "10.4": "Miss detection",
    "10.5": "Automated response to misses", "10.6": "Calibration metrics",
    "11": "CLI specification", "11.1": "Commands", "11.2": "Exit codes", "11.3": "CLI output",
    "12": "REST API", "12.1": "Endpoints", "12.2": "Planning request shape",
    "12.3": "Error response", "12.4": "Idempotency and retries",
    "13": "Persistence design", "13.1": "Required tables", "13.2": "Indexes and constraints",
    "13.3": "Graph query strategy", "14": "Security, privacy and supply-chain requirements",
    "15": "Observability and operating model", "15.1": "Required metrics", "15.2": "Tracing",
    "15.3": "Alerting", "15.4": "Suggested initial SLOs",
    "16": "Failure handling and fallback matrix", "17": "Testing strategy",
    "17.1": "Unit tests", "17.2": "Adapter contract tests", "17.3": "Integration tests",
    "17.4": "Planner correctness tests", "17.5": "Audit and safety tests",
    "17.6": "Database and API tests", "17.7": "Performance and load tests",
    "17.8": "End-to-end historical replay benchmark", "17.9": "Regression and release gates",
    "18": "Operational deployment", "18.1": "Single-node/self-hosted deployment",
    "18.2": "Kubernetes deployment", "18.3": "Backup and recovery", "18.4": "Upgrade policy",
    "19": "Performance and correctness trade-offs", "20": "Repository layout for the implementation agent",
    "21": "Implementation order and completion gates", "22": "Acceptance criteria",
    "23": "Failure scenarios the implementation agent must simulate",
    "24": "Reference material", "Appendix-A": "Canonical reason codes",
    "Appendix-B": "Canonical outcome semantics", "Appendix-C": "Algorithm pseudocode",
    "Appendix-D": "Definition of done for the AI implementation agent",
}

# Sections whose lists define implementation, interface, safety, testing, or
# operational acceptance criteria even when the individual bullet omits MUST.
STRUCTURED_PREFIXES = (
    "0", "1.4", "1.5", "2", "3", "4", "5", "6", "7", "8", "9",
    "10", "11", "12", "13", "14", "15", "16", "17", "18", "19",
    "20", "21", "22", "23", "Appendix-A", "Appendix-B", "Appendix-C",
    "Appendix-D",
)


def canonical(text: str) -> str:
    text = re.sub(r"\s+", " ", text).strip()
    text = re.sub(r"\s*\|\s*", " | ", text)
    return text


def section_key(match: re.Match[str]) -> tuple[str, str]:
    raw = match.group(1).strip().rstrip(".")
    if raw.startswith("Appendix"):
        key = raw.replace("Appendix ", "Appendix-")
    else:
        key = raw
    return key, match.group(2).strip()


def in_structured_section(section: str) -> bool:
    return any(section == prefix or section.startswith(prefix + ".") for prefix in STRUCTURED_PREFIXES)


def source_requirements(source: str) -> list[dict[str, object]]:
    records: list[dict[str, object]] = []
    section = "0"
    section_title = "Normative implementation instructions"
    fenced = False
    fence_line = 0
    fence_contents: list[tuple[int, str]] = []
    pending: list[str] = []
    pending_line = 0

    def emit(text: str, line_no: int, force: bool = False) -> None:
        text = canonical(text)
        if not text or text.startswith("|") and set(text.replace("|", "").replace("-", "").replace(":", "").strip()) == set():
            return
        if not force and not NORMATIVE.search(text):
            return
        pieces = [canonical(piece) for piece in SENTENCE.split(text) if canonical(piece)]
        if not pieces:
            pieces = [text]
        for piece in pieces:
            records.append({
                "section": section,
                "section_title": section_title,
                "source_line": line_no,
                "text": piece,
            })

    def flush(force: bool = False) -> None:
        nonlocal pending, pending_line
        if pending:
            emit(" ".join(pending), pending_line, force)
            pending = []
            pending_line = 0

    lines = source.splitlines()
    for index, raw in enumerate(lines):
        line_no = index + 1
        stripped = raw.strip()
        if stripped.startswith("```"):
            flush()
            if not fenced:
                fenced = True
                fence_line = line_no
                fence_contents = []
            else:
                if section.startswith("4."):
                    for type_index, type_line in enumerate(fence_contents):
                        type_match = re.match(r"\s*type\s+(\w+)\s+struct\s*\{", type_line[1])
                        if not type_match:
                            continue
                        type_name = type_match.group(1)
                        field_lines: list[tuple[int, str]] = []
                        for field_line_no, candidate in fence_contents[type_index + 1:]:
                            if candidate.strip() == "}":
                                break
                            field_match = re.match(r"\s+([A-Z][A-Za-z0-9_]*)\s+([^/]+?)(?:\s*//.*)?$", candidate)
                            if field_match:
                                field_lines.append((field_line_no, f"{field_match.group(1)} {field_match.group(2).strip()}"))
                        for field_line_no, field in field_lines:
                            emit(f"The LLD {type_name} data model includes the {field} field.", field_line_no, force=True)
                elif section == "5.2":
                    stack: list[tuple[int, str]] = []
                    for config_line_no, config_line in fence_contents:
                        key_match = re.match(r"^(\s*)([A-Za-z0-9_-]+):(?:\s*(.*))?$", config_line)
                        if not key_match:
                            continue
                        indent = len(key_match.group(1).replace("\t", "  "))
                        key = key_match.group(2)
                        value = (key_match.group(3) or "").strip()
                        while stack and stack[-1][0] >= indent:
                            stack.pop()
                        path = ".".join([part for _, part in stack] + [key])
                        if value:
                            emit(f"The LLD default configuration defines `{path}` as `{value}`.", config_line_no, force=True)
                        else:
                            next_child_is_list = any(
                                candidate.strip().startswith("-")
                                and len(candidate) - len(candidate.lstrip()) > indent
                                for _, candidate in fence_contents[fence_contents.index((config_line_no, config_line)) + 1:]
                            )
                            if next_child_is_list:
                                emit(f"The LLD default configuration defines `{path}` as a list.", config_line_no, force=True)
                            stack.append((indent, key))
                elif section == "Appendix-C":
                    excerpt = " ".join(line.strip() for _, line in fence_contents if line.strip())
                    if excerpt:
                        records.append({
                            "section": section,
                            "section_title": section_title,
                            "source_line": fence_line + 1,
                            "text": "Implement the planner algorithm in this LLD pseudocode without changing its decision behavior: " + canonical(excerpt),
                        })
                fenced = False
            continue
        if fenced:
            fence_contents.append((line_no, raw))
            continue

        heading = HEADING.match(stripped)
        if heading:
            candidate_section, candidate_title = section_key(heading)
            if HEADING_TITLES.get(candidate_section) == candidate_title:
                flush()
                section, section_title = candidate_section, candidate_title
                continue
        if not stripped:
            flush()
            continue

        if section == "Appendix-C":
            flush()
            emit(stripped, line_no, force=True)
            continue

        # Keep Markdown table rows independent so a normative table cell does
        # not accidentally absorb adjacent rows into one requirement.
        if stripped.startswith("|"):
            flush()
            if re.fullmatch(r"\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?", stripped):
                continue
            if index + 1 < len(lines) and re.fullmatch(
                r"\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?", lines[index + 1].strip()
            ):
                continue
            if in_structured_section(section) or section == "1.4":
                emit(stripped, line_no, force=True)
            else:
                emit(stripped, line_no)
            continue

        if section == "1.4" and "\t" in raw:
            flush()
            emit(stripped.replace("\t", " | "), line_no, force=True)
            continue

        if section == "12.1" and re.match(r"^(?:GET|POST|PUT|PATCH|DELETE)\s+\/", stripped):
            flush()
            emit(stripped, line_no, force=True)
            continue

        item = LIST_ITEM.match(raw)
        if item:
            flush()
            is_algorithm_stage = bool(re.match(r"Stage\s+\d+\s*[—-]", item.group(1)))
            force = in_structured_section(section) or is_algorithm_stage
            emit(item.group(1), line_no, force=force)
            continue

        if pending and (raw.startswith(" ") or raw.startswith("\t")):
            pending.append(stripped)
        else:
            flush()
            pending = [stripped]
            pending_line = line_no
    flush()

    # The LLD endpoint table and canonical enumerations are contracts in their
    # own right. Ensure endpoint rows and algorithm-stage labels are extracted
    # even if their Markdown formatting changes.
    seen: set[tuple[str, str]] = set()
    unique: list[dict[str, object]] = []
    for record in records:
        key = (str(record["section"]), canonical(str(record["text"])).casefold())
        if key in seen:
            continue
        seen.add(key)
        unique.append(record)
    return unique


def stable_id(section: str, text: str) -> str:
    section_slug = re.sub(r"[^A-Za-z0-9]+", "-", section).strip("-").upper()
    digest = hashlib.sha256(f"{section}\n{canonical(text)}".encode("utf-8")).hexdigest()[:10]
    return f"IVP-{section_slug}-{digest}"


def load_previous() -> dict[str, dict[str, object]]:
    if not INVENTORY.exists():
        return {}
    try:
        data = json.loads(INVENTORY.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SystemExit(f"cannot read {INVENTORY}: {exc}") from exc
    return {str(item["id"]): item for item in data.get("requirements", []) if isinstance(item, dict) and "id" in item}


def build_inventory(source: str, previous: dict[str, dict[str, object]]) -> dict[str, object]:
    requirements: list[dict[str, object]] = []
    for candidate in source_requirements(source):
        text = str(candidate["text"])
        section = str(candidate["section"])
        req_id = stable_id(section, text)
        old = previous.get(req_id, {})
        status = str(old.get("status", "UNREVIEWED"))
        if status not in STATUSES:
            status = "UNREVIEWED"
        # Migrate the original coarse baseline: empty IN_PROGRESS rows had not
        # yet been reviewed and must not masquerade as assessed gaps.
        if status == "IN_PROGRESS" and not any(old.get(key) for key in ("implementation", "verification", "evidence", "limitations")):
            status = "UNREVIEWED"
        requirements.append({
            "id": req_id,
            **candidate,
            "status": status,
            "implementation": old.get("implementation", []),
            "verification": old.get("verification", []),
            "evidence": old.get("evidence", []),
            "limitations": old.get("limitations", []),
        })
    digest = hashlib.sha256(source.encode("utf-8")).hexdigest()
    return {
        "schema_version": 1,
        "source": "docs/Incremental_Verification_Planner_LLD.md",
        "source_sha256": digest,
        "scoring": {
            "implementation_coverage": "lower bound: requirements with status IMPLEMENTED or TESTED divided by total requirements; unreviewed requirements count as incomplete",
            "verified_completion": "lower bound: requirements with status TESTED divided by total requirements; unreviewed requirements count as unverified",
            "assessment_coverage": "requirements with a status other than UNREVIEWED divided by total requirements",
            "weighting": "equal per independently verifiable requirement",
            "complete_when": "all requirements are TESTED with passing evidence",
        },
        "requirements": requirements,
    }


def percent(numerator: int, denominator: int) -> str:
    return f"{(100.0 * numerator / denominator) if denominator else 0.0:.1f}%"


def section_sort_key(section: str) -> tuple[tuple[int, object], ...]:
    parts = section.split(".")
    return tuple((0, int(part)) if part.isdigit() else (1, part) for part in parts)


def tracker_markdown(inventory: dict[str, object]) -> str:
    requirements = inventory["requirements"]
    assert isinstance(requirements, list)
    by_section: dict[str, dict[str, int]] = {}
    for item in requirements:
        assert isinstance(item, dict)
        counts = by_section.setdefault(str(item["section"]), {status: 0 for status in STATUSES})
        counts[str(item["status"])] += 1

    total = len(requirements)
    implemented = sum(item["status"] in ("IMPLEMENTED", "TESTED") for item in requirements)
    tested = sum(item["status"] == "TESTED" for item in requirements)
    assessed = sum(item["status"] != "UNREVIEWED" for item in requirements)
    lines = [
        "# IVP implementation tracker",
        "",
        "Generated from `docs/Incremental_Verification_Planner_LLD.md` by `scripts/generate-tracker.py`.",
        "Atomic requirement details, code references, validation commands, and evidence are maintained in [`requirements-inventory.json`](requirements-inventory.json).",
        "",
        "## Current completion scores",
        "",
        f"- Implementation coverage (lower bound): **{percent(implemented, total)}** ({implemented}/{total} requirements marked `IMPLEMENTED` or `TESTED`).",
        f"- Verified completion (lower bound): **{percent(tested, total)}** ({tested}/{total} requirements marked `TESTED`).",
        f"- Assessment coverage: **{percent(assessed, total)}** ({assessed}/{total} requirements reviewed against code and evidence).",
        "- `UNREVIEWED` means no requirement-level assessment has been recorded. Lower-bound scores count unreviewed requirements as incomplete and do not claim those requirements have no related code.",
        "- A requirement is `TESTED` only after its listed validation passes and evidence is recorded. Unrun or environment-blocked checks do not count as verified.",
        "",
        "## Status by LLD section",
        "",
        "| LLD section | Requirements | Unreviewed | Not started | In progress | Implemented | Tested | Blocked |",
        "|---|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for section in sorted(by_section, key=section_sort_key):
        counts = by_section[section]
        lines.append(
            f"| {section} | {sum(counts.values())} | {counts['UNREVIEWED']} | {counts['NOT_STARTED']} | {counts['IN_PROGRESS']} | "
            f"{counts['IMPLEMENTED']} | {counts['TESTED']} | {counts['BLOCKED']} |"
        )
    lines.extend([
        "",
        "## Tracker rules",
        "",
        "- `UNREVIEWED` is an inventory baseline state; set a reviewed status only after inspecting the requirement's implementation and validation evidence.",
        "- `IMPLEMENTED` requires a reviewed implementation reference that performs the stated behavior.",
        "- `TESTED` requires a passing validation command and recorded evidence; code inspection alone is insufficient.",
        "- `BLOCKED` records the specific environmental or external dependency preventing implementation or validation.",
        "- Run `python scripts/generate-tracker.py` after editing inventory status/evidence fields. Run `python scripts/generate-tracker.py --check` to verify generated files are current.",
        "",
    ])
    return "\n".join(lines)


def write_or_check(path: Path, content: str, check: bool) -> bool:
    current = path.read_text(encoding="utf-8") if path.exists() else None
    if current == content:
        return True
    if check:
        print(f"out of date: {path.relative_to(ROOT).as_posix()}", file=sys.stderr)
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8", newline="\n")
    print(f"updated {path.relative_to(ROOT).as_posix()}")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail if generated inventory or tracker is stale")
    args = parser.parse_args()
    source = LLD.read_text(encoding="utf-8")
    inventory = build_inventory(source, load_previous())
    inventory_text = json.dumps(inventory, ensure_ascii=False, indent=2) + "\n"
    tracker_text = tracker_markdown(inventory)
    results = [
        write_or_check(INVENTORY, inventory_text, args.check),
        write_or_check(TRACKER, tracker_text, args.check),
    ]
    print(f"requirements: {len(inventory['requirements'])}")
    return 0 if all(results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
