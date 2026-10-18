package main

import (
	"testing"
	"time"
)

func TestDurationFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"unset uses default", "", 7 * time.Second},
		{"valid duration", "250ms", 250 * time.Millisecond},
		{"garbage uses default", "soon", 7 * time.Second},
		{"zero uses default", "0s", 7 * time.Second},
		{"negative uses default", "-1s", 7 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_TIMEOUT", tc.value)

			if got := durationFromEnv("TEST_TIMEOUT", 7*time.Second); got != tc.want {
				t.Errorf("durationFromEnv(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}
