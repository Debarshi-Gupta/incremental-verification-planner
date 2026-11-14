# IVP implementation tracker

Generated from `docs/Incremental_Verification_Planner_LLD.md` by `scripts/generate-tracker.py`.
Atomic requirement details, code references, validation commands, and evidence are maintained in [`requirements-inventory.json`](requirements-inventory.json).

## Current completion scores

- Implementation coverage (lower bound): **100.0%** (853/853 requirements marked `IMPLEMENTED` or `TESTED`).
- Verified completion (lower bound): **55.8%** (476/853 requirements marked `TESTED`).
- Assessment coverage: **100.0%** (853/853 requirements reviewed against code and evidence).
- `UNREVIEWED` means no requirement-level assessment has been recorded. Lower-bound scores count unreviewed requirements as incomplete and do not claim those requirements have no related code.
- A requirement is `TESTED` only after its listed validation passes and evidence is recorded. Unrun or environment-blocked checks do not count as verified.

## Status by LLD section

| LLD section | Requirements | Unreviewed | Not started | In progress | Implemented | Tested | Blocked |
|---|---:|---:|---:|---:|---:|---:|---:|
| 0 | 24 | 0 | 0 | 0 | 9 | 15 | 0 |
| 1.1 | 2 | 0 | 0 | 0 | 2 | 0 | 0 |
| 1.4 | 13 | 0 | 0 | 0 | 4 | 9 | 0 |
| 1.5 | 6 | 0 | 0 | 0 | 4 | 2 | 0 |
| 1.6 | 1 | 0 | 0 | 0 | 1 | 0 | 0 |
| 2.2 | 17 | 0 | 0 | 0 | 7 | 10 | 0 |
| 2.3 | 9 | 0 | 0 | 0 | 5 | 4 | 0 |
| 3.2 | 25 | 0 | 0 | 0 | 7 | 18 | 0 |
| 4 | 4 | 0 | 0 | 0 | 2 | 2 | 0 |
| 4.2 | 3 | 0 | 0 | 0 | 2 | 1 | 0 |
| 4.3 | 3 | 0 | 0 | 0 | 0 | 3 | 0 |
| 4.4 | 12 | 0 | 0 | 0 | 4 | 8 | 0 |
| 4.5 | 6 | 0 | 0 | 0 | 4 | 2 | 0 |
| 4.6 | 2 | 0 | 0 | 0 | 1 | 1 | 0 |
| 4.7 | 3 | 0 | 0 | 0 | 1 | 2 | 0 |
| 5.1 | 8 | 0 | 0 | 0 | 2 | 6 | 0 |
| 5.2 | 36 | 0 | 0 | 0 | 0 | 36 | 0 |
| 6.1 | 6 | 0 | 0 | 0 | 1 | 5 | 0 |
| 6.2 | 7 | 0 | 0 | 0 | 1 | 6 | 0 |
| 6.3 | 77 | 0 | 0 | 0 | 40 | 37 | 0 |
| 7.1 | 14 | 0 | 0 | 0 | 5 | 9 | 0 |
| 7.2 | 9 | 0 | 0 | 0 | 4 | 5 | 0 |
| 7.3 | 5 | 0 | 0 | 0 | 3 | 2 | 0 |
| 7.5 | 3 | 0 | 0 | 0 | 1 | 2 | 0 |
| 8.1 | 5 | 0 | 0 | 0 | 0 | 5 | 0 |
| 8.2 | 10 | 0 | 0 | 0 | 8 | 2 | 0 |
| 8.3 | 9 | 0 | 0 | 0 | 4 | 5 | 0 |
| 8.4 | 9 | 0 | 0 | 0 | 4 | 5 | 0 |
| 8.5 | 9 | 0 | 0 | 0 | 4 | 5 | 0 |
| 8.6 | 7 | 0 | 0 | 0 | 3 | 4 | 0 |
| 8.7 | 6 | 0 | 0 | 0 | 4 | 2 | 0 |
| 8.8 | 10 | 0 | 0 | 0 | 3 | 7 | 0 |
| 8.9 | 4 | 0 | 0 | 0 | 2 | 2 | 0 |
| 9.1 | 10 | 0 | 0 | 0 | 3 | 7 | 0 |
| 9.2 | 4 | 0 | 0 | 0 | 1 | 3 | 0 |
| 9.3 | 5 | 0 | 0 | 0 | 2 | 3 | 0 |
| 9.4 | 10 | 0 | 0 | 0 | 7 | 3 | 0 |
| 10.1 | 2 | 0 | 0 | 0 | 1 | 1 | 0 |
| 10.2 | 5 | 0 | 0 | 0 | 0 | 5 | 0 |
| 10.3 | 2 | 0 | 0 | 0 | 0 | 2 | 0 |
| 10.4 | 5 | 0 | 0 | 0 | 0 | 5 | 0 |
| 10.5 | 7 | 0 | 0 | 0 | 5 | 2 | 0 |
| 10.6 | 8 | 0 | 0 | 0 | 2 | 6 | 0 |
| 11.1 | 20 | 0 | 0 | 0 | 17 | 3 | 0 |
| 11.3 | 2 | 0 | 0 | 0 | 2 | 0 | 0 |
| 12 | 3 | 0 | 0 | 0 | 1 | 2 | 0 |
| 12.1 | 18 | 0 | 0 | 0 | 11 | 7 | 0 |
| 12.2 | 2 | 0 | 0 | 0 | 1 | 1 | 0 |
| 12.3 | 1 | 0 | 0 | 0 | 0 | 1 | 0 |
| 12.4 | 4 | 0 | 0 | 0 | 1 | 3 | 0 |
| 13 | 3 | 0 | 0 | 0 | 1 | 2 | 0 |
| 13.1 | 20 | 0 | 0 | 0 | 19 | 1 | 0 |
| 13.2 | 9 | 0 | 0 | 0 | 8 | 1 | 0 |
| 13.3 | 5 | 0 | 0 | 0 | 1 | 4 | 0 |
| 14 | 18 | 0 | 0 | 0 | 12 | 6 | 0 |
| 15.1 | 16 | 0 | 0 | 0 | 0 | 16 | 0 |
| 15.3 | 8 | 0 | 0 | 0 | 5 | 3 | 0 |
| 15.4 | 8 | 0 | 0 | 0 | 3 | 5 | 0 |
| 17.1 | 12 | 0 | 0 | 0 | 0 | 12 | 0 |
| 17.2 | 11 | 0 | 0 | 0 | 2 | 9 | 0 |
| 17.3 | 10 | 0 | 0 | 0 | 0 | 10 | 0 |
| 17.4 | 11 | 0 | 0 | 0 | 5 | 6 | 0 |
| 17.5 | 8 | 0 | 0 | 0 | 0 | 8 | 0 |
| 17.6 | 6 | 0 | 0 | 0 | 2 | 4 | 0 |
| 17.8 | 7 | 0 | 0 | 0 | 1 | 6 | 0 |
| 17.9 | 3 | 0 | 0 | 0 | 2 | 1 | 0 |
| 18.1 | 7 | 0 | 0 | 0 | 5 | 2 | 0 |
| 18.2 | 13 | 0 | 0 | 0 | 13 | 0 | 0 |
| 18.3 | 4 | 0 | 0 | 0 | 4 | 0 | 0 |
| 18.4 | 4 | 0 | 0 | 0 | 1 | 3 | 0 |
| 19 | 21 | 0 | 0 | 0 | 8 | 13 | 0 |
| 20 | 5 | 0 | 0 | 0 | 5 | 0 | 0 |
| 21 | 12 | 0 | 0 | 0 | 5 | 7 | 0 |
| 22 | 21 | 0 | 0 | 0 | 4 | 17 | 0 |
| 23 | 18 | 0 | 0 | 0 | 9 | 9 | 0 |
| Appendix-A | 47 | 0 | 0 | 0 | 37 | 10 | 0 |
| Appendix-B | 10 | 0 | 0 | 0 | 2 | 8 | 0 |
| Appendix-C | 52 | 0 | 0 | 0 | 23 | 29 | 0 |
| Appendix-D | 12 | 0 | 0 | 0 | 4 | 8 | 0 |

## Tracker rules

- `UNREVIEWED` is an inventory baseline state; set a reviewed status only after inspecting the requirement's implementation and validation evidence.
- `IMPLEMENTED` requires a reviewed implementation reference that performs the stated behavior.
- `TESTED` requires a passing validation command and recorded evidence; code inspection alone is insufficient.
- `BLOCKED` records the specific environmental or external dependency preventing implementation or validation.
- Run `python scripts/generate-tracker.py` after editing inventory status/evidence fields. Run `python scripts/generate-tracker.py --check` to verify generated files are current.
