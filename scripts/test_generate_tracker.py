import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("generate-tracker.py")
SPEC = importlib.util.spec_from_file_location("ivp_tracker", SCRIPT)
TRACKER = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(TRACKER)


class RequirementInventoryTests(unittest.TestCase):
    def test_numbered_lists_do_not_become_section_headings(self):
        source = """0. Normative implementation instructions
1. Build the product.
2. Preserve all mandatory checks.
1. Product definition
1.1 Problem
The planner MUST remain conservative.
"""
        records = TRACKER.source_requirements(source)
        self.assertEqual([record["section"] for record in records], ["0", "0", "1.1"])

    def test_api_and_database_contract_rows_are_atomic(self):
        source = """12. REST API
12.1 Endpoints
GET /health/live\tLiveness\tpublic
13. Persistence design
13.1 Required tables
1. tenants(id, name)
2. repositories(id, tenant_id)
"""
        records = TRACKER.source_requirements(source)
        self.assertEqual([record["section"] for record in records], ["12.1", "13.1", "13.1"])
        self.assertIn("GET /health/live", records[0]["text"])

    def test_domain_fields_and_algorithm_pseudocode_are_inventoried(self):
        source = """4. Core domain model
4.1 Repository
```go
type Repository struct {
    ID UUID
    Slug string
}
```
Appendix C. Algorithm pseudocode
function createPlan(request):
    return fullRequired(reason)
Appendix D. Definition of done for the AI implementation agent
"""
        records = TRACKER.source_requirements(source)
        texts = [record["text"] for record in records]
        self.assertTrue(any("Repository data model includes the ID UUID field" in text for text in texts))
        self.assertTrue(any("Appendix-C" == record["section"] and "return fullRequired" in record["text"] for record in records))

    def test_requirement_ids_are_stable(self):
        first = TRACKER.stable_id("8.2", "Gradle MUST parse JUnit reports.")
        second = TRACKER.stable_id("8.2", "Gradle MUST parse JUnit reports.")
        self.assertEqual(first, second)

    def test_unreviewed_inventory_is_reported_separately_from_coverage(self):
        inventory = {"requirements": [
            {"section": "8.2", "status": "UNREVIEWED"},
            {"section": "8.2", "status": "IMPLEMENTED"},
            {"section": "8.2", "status": "TESTED"},
        ]}
        tracker = TRACKER.tracker_markdown(inventory)
        self.assertIn("Implementation coverage (lower bound): **66.7%** (2/3", tracker)
        self.assertIn("Verified completion (lower bound): **33.3%** (1/3", tracker)
        self.assertIn("Assessment coverage: **66.7%** (2/3", tracker)

    def test_empty_legacy_in_progress_rows_migrate_to_unreviewed(self):
        source = "0. Normative implementation instructions\nThe product MUST work.\n"
        req = TRACKER.source_requirements(source)[0]
        req_id = TRACKER.stable_id(req["section"], req["text"])
        inventory = TRACKER.build_inventory(source, {req_id: {"status": "IN_PROGRESS", "implementation": [], "verification": [], "evidence": [], "limitations": []}})
        self.assertEqual(inventory["requirements"][0]["status"], "UNREVIEWED")


if __name__ == "__main__":
    unittest.main()
