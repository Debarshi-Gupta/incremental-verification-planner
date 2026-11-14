package persistence

import "testing"

// LLD 7.3: flakiness is classified only after a minimum number of comparable
// observations. flakeRate must not derive a rate from insufficient evidence —
// a 1-sample flake rate of 1.0 poisons selection reliability — while duration
// aggregates remain a separate, ungated concern (LLD line 226).
func TestFlakeRateRequiresMinimumComparableObservations(t *testing.T) {
	cases := []struct {
		name            string
		flaky, samples  int64
		minObservations int
		want            float64
	}{
		{name: "single flaky sample below minimum claims nothing", flaky: 1, samples: 1, minObservations: 5, want: 0},
		{name: "all samples flaky but below minimum claims nothing", flaky: 4, samples: 4, minObservations: 5, want: 0},
		{name: "boundary exactly at minimum computes exact rate", flaky: 1, samples: 5, minObservations: 5, want: 0.2},
		{name: "above the minimum computes exact rate", flaky: 3, samples: 10, minObservations: 5, want: 0.3},
		{name: "no flaky samples yields zero at the minimum", flaky: 0, samples: 5, minObservations: 5, want: 0},
		{name: "no flaky samples yields zero above the minimum", flaky: 0, samples: 8, minObservations: 5, want: 0},
		{name: "no samples yields zero", flaky: 0, samples: 0, minObservations: 5, want: 0},
		{name: "lower minimum is honored at its own boundary", flaky: 1, samples: 2, minObservations: 2, want: 0.5},
		{name: "non-positive minimum fails closed", flaky: 1, samples: 1, minObservations: 0, want: 0},
		{name: "negative minimum fails closed", flaky: 5, samples: 5, minObservations: -1, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flakeRate(tc.flaky, tc.samples, tc.minObservations)
			if got != tc.want {
				t.Fatalf("flakeRate(%d, %d, %d) = %v, want %v", tc.flaky, tc.samples, tc.minObservations, got, tc.want)
			}
		})
	}
}
