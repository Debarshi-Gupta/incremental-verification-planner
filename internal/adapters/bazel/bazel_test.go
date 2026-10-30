package bazel

import "testing"

func TestParseBEPReportsPassFailAndZeroTests(t *testing.T) {
	got, err := parseBEP([]byte(`{"id":{"testSummary":{"label":"//app:test"}},"testSummary":{"overallStatus":"FAILED","totalRunCount":2,"totalNumTests":2,"passed":1,"failed":1}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Tests != 2 || got.Passed != 1 || got.Failed != 1 {
		t.Fatalf("unexpected BEP result: %+v", got)
	}
	zero, err := parseBEP([]byte(`{"testSummary":{"overallStatus":"NO_STATUS","totalRunCount":0}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !zero.ZeroTests {
		t.Fatalf("zero-test BEP not detected: %+v", zero)
	}
	if _, err = parseBEP([]byte("{broken"), 0); err == nil {
		t.Fatal("malformed BEP accepted")
	}
}
