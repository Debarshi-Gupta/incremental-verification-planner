package jest

import (
	"context"
	"testing"

	"github.com/ivp/ivp/internal/adapters"
)

func TestJestJSONResultParser(t *testing.T) {
	data := []byte(`{"numTotalTests":2,"numPassedTests":1,"numFailedTests":1,"numPendingTests":0,"numTodoTests":0,"testResults":[{"name":"tests/a.test.js","assertionResults":[{"fullName":"works","status":"passed","duration":3},{"fullName":"fails","status":"failed","duration":7,"failureMessages":["expected 1"]}]}]}`)
	got, err := New().ParseResults(context.Background(), adapters.ParseRequest{Stdout: data, ExitCode: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Tests != 2 || got.Passed != 1 || got.Failed != 1 || len(got.Cases) != 2 {
		t.Fatalf("unexpected Jest parse: %+v", got)
	}
}
