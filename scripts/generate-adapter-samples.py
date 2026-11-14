"""Publish the adapter sample reports required by LLD Appendix D.

The checked-in fixture corpus holds a successful and a failed report for every
adapter. This script copies those two reports per adapter into
docs/adapter-samples/ so the documented sample reports are the very reports the
contract test consumes, and it records the shared contract they follow.

Usage:
    python scripts/generate-adapter-samples.py          # regenerate the samples
    python scripts/generate-adapter-samples.py --check  # verify they are current
"""
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = ROOT / "internal" / "adapters" / "fixtures"
SAMPLES = ROOT / "docs" / "adapter-samples"

# The two categories the Appendix-D row asks for: one successful and one
# failed report per adapter.
SELECTION = (("full_suite", "success"), ("failure", "failure"))


def adapter_samples() -> list[tuple[str, Path, Path]]:
    """Return (adapter, source, target) triples for the published samples.

    The corpus is laid out as <adapter>/<tool version>/<category>.<ext>; the
    newest tool version of each adapter provides the published sample.
    """
    found = []
    for adapter_dir in sorted(p for p in FIXTURES.iterdir() if p.is_dir()):
        versions = sorted((p for p in adapter_dir.iterdir() if p.is_dir()), key=lambda p: p.name)
        if not versions:
            continue
        version = versions[-1]
        for category, label in SELECTION:
            matches = sorted(version.glob(category + ".*"))
            if not matches:
                continue
            source = matches[0]
            found.append((adapter_dir.name, source, SAMPLES / f"{adapter_dir.name}-{label}{source.suffix}"))
    return found


def main() -> int:
    check = "--check" in sys.argv
    SAMPLES.mkdir(parents=True, exist_ok=True)
    stale = []
    for adapter, source, target in adapter_samples():
        if check:
            if not target.exists() or target.read_bytes() != source.read_bytes():
                stale.append(str(target.relative_to(ROOT)))
            continue
        shutil.copyfile(source, target)
        print(f"published {target.relative_to(ROOT)}")
    if check:
        if stale:
            print("ADAPTER SAMPLES ARE STALE:")
            for path in stale:
                print(" -", path)
            return 1
        print(f"adapter samples OK: {len(adapter_samples())} sample reports are current")
        return 0
    return 0


if __name__ == "__main__":
    sys.exit(main())
