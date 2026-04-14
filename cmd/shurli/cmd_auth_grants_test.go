package main

import (
	"strings"
	"testing"
)

// TestNormalizeAuthGrantFlags covers the mutual-exclusion + pre-validation
// logic for `shurli auth grant` flag combinations. This runs fully offline
// so it stays green even when the live daemon socket is unavailable.
func TestNormalizeAuthGrantFlags(t *testing.T) {
	cases := []struct {
		name          string
		service       string
		services      string
		budget        string
		bandwidth     string
		transport     string
		wantServices  string
		wantBudget    string
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:         "empty",
			wantServices: "",
			wantBudget:   "",
		},
		{
			name:         "service alone promotes to services",
			service:      "file-download",
			wantServices: "file-download",
		},
		{
			name:         "services alone",
			services:     "file-browse,file-download",
			wantServices: "file-browse,file-download",
		},
		{
			name:         "service+services agree",
			service:      "file-download",
			services:     "file-download",
			wantServices: "file-download",
		},
		{
			name:          "service+services disagree rejects",
			service:       "file-download",
			services:      "file-browse",
			wantErr:       true,
			wantErrSubstr: "--service and --services",
		},
		{
			name:       "bandwidth alone",
			bandwidth:  "500MB",
			wantBudget: "500MB",
		},
		{
			name:       "budget alone",
			budget:     "20GB",
			wantBudget: "20GB",
		},
		{
			name:       "budget+bandwidth agree",
			budget:     "20GB",
			bandwidth:  "20GB",
			wantBudget: "20GB",
		},
		{
			name:          "budget+bandwidth disagree rejects",
			budget:        "20GB",
			bandwidth:     "1GB",
			wantErr:       true,
			wantErrSubstr: "--budget and --bandwidth",
		},
		{
			name:          "bad byte size rejects",
			budget:        "not-a-size",
			wantErr:       true,
			wantErrSubstr: "invalid budget",
		},
		{
			name:      "transport ok",
			transport: "lan,direct,relay",
		},
		{
			name:          "transport bad",
			transport:    "wifi",
			wantErr:      true,
			wantErrSubstr: "invalid --transport",
		},
		{
			name:       "full valid combination",
			service:    "file-download",
			budget:     "20GB",
			transport:  "lan,direct,relay",
			wantServices: "file-download",
			wantBudget:   "20GB",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSvc, gotBud, err := normalizeAuthGrantFlags(tc.service, tc.services, tc.budget, tc.bandwidth, tc.transport)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotSvc != tc.wantServices {
				t.Errorf("services = %q, want %q", gotSvc, tc.wantServices)
			}
			if gotBud != tc.wantBudget {
				t.Errorf("budget = %q, want %q", gotBud, tc.wantBudget)
			}
		})
	}
}
