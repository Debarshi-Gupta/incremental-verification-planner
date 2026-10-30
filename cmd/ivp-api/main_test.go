package main

import (
	"testing"
)

func TestRetentionDaysUsesSafeDefaultAndBounds(t *testing.T) {
	cases := []struct {
		input string
		want  int
		bad   bool
	}{
		{input: "", want: 90},
		{input: "30", want: 30},
		{input: "0", bad: true},
		{input: "36501", bad: true},
		{input: "forever", bad: true},
	}
	for _, tc := range cases {
		got, err := retentionDays(tc.input)
		if tc.bad {
			if err == nil {
				t.Errorf("retentionDays(%q) = %d, want error", tc.input, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("retentionDays(%q) = %d, %v; want %d, nil", tc.input, got, err, tc.want)
		}
	}
}

func TestDevelopmentAuthRequiresDevelopmentEnvironment(t *testing.T) {
	t.Setenv("IVP_ENV", "production")
	t.Setenv("IVP_DEV_AUTH", "1")
	if developmentAuthEnabled() {
		t.Fatal("development authentication enabled in production")
	}
	t.Setenv("IVP_ENV", "development")
	t.Setenv("IVP_DEV_AUTH", "0")
	if developmentAuthEnabled() {
		t.Fatal("development authentication enabled without explicit opt-in")
	}
	t.Setenv("IVP_DEV_AUTH", "1")
	if !developmentAuthEnabled() {
		t.Fatal("development authentication did not activate for explicit development opt-in")
	}
}
