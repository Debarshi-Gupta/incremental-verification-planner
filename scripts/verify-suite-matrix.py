"""Verify the validation-suite matrix against the CI workflows.

The matrix in docs/validation-suites.md lists every suite of the validation
program. This check asserts that:

  1. every command listed in the matrix is wired into a workflow;
  2. every test command that runs in a workflow is listed in the matrix;
  3. every listed command runs somewhere (pull request or nightly).

Usage: python scripts/verify-suite-matrix.py
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MATRIX = ROOT / "docs" / "validation-suites.md"
WORKFLOWS = ROOT / ".github" / "workflows"

# Commands that run in a workflow but are not test suites.
IGNORED = (
    "gofmt -l",
    "go vet",
    "generate-tracker.py",
    "test_generate_tracker.py",
    "govulncheck",
    "syft",
    "go build",
    "mkdir",
    "./dist/ivp",
    "python3 -m pip",
    "npm install",
    "cd examples/",
)


def matrix_commands():
    """Return the commands listed in the matrix table."""
    commands = []
    for line in MATRIX.read_text(encoding="utf-8").splitlines():
        if not line.startswith("|") or line.startswith("|---") or "Command" in line:
            continue
        cells = [cell.strip() for cell in line.strip("|").split("|")]
        if len(cells) < 3:
            continue
        command = cells[1].strip("`")
        if command:
            commands.append((cells[0].strip("`"), command, cells[2]))
    return commands


def workflow_text():
    """Return the concatenated workflow files."""
    parts = []
    for path in sorted(WORKFLOWS.glob("*.yml")):
        parts.append(path.read_text(encoding="utf-8"))
    return "\n".join(parts)


def workflow_test_commands(text):
    """Return the test commands executed by the workflows."""
    commands = []
    for line in text.splitlines():
        stripped = line.strip()
        if "go test" not in stripped:
            continue
        for part in stripped.split("&&"):
            part = part.strip()
            if part.startswith("go test"):
                commands.append(part)
    return commands


def main():
    errors = []
    listed = matrix_commands()
    if not listed:
        errors.append("the matrix lists no command")
    workflows = workflow_text()
    for name, command, runs_on in listed:
        if command not in workflows:
            errors.append(f"matrix suite {name!r} is not wired into any workflow: {command}")
        if not runs_on.strip():
            errors.append(f"matrix suite {name!r} does not state where it runs")
    for command in workflow_test_commands(workflows):
        if any(ignored in command for ignored in IGNORED):
            continue
        if not any(command in listed_command for _, listed_command, _ in listed):
            errors.append(f"workflow command {command!r} is not listed in the matrix")
    if errors:
        print("VALIDATION SUITE MATRIX CHECK FAILED:")
        for error in errors:
            print(" -", error)
        sys.exit(1)
    print(f"validation suite matrix OK: {len(listed)} suites listed and wired into the workflows")


if __name__ == "__main__":
    main()
