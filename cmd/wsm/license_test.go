package main

import (
	"strings"
	"testing"

	"github.com/wandb/wsm/pkg/license"
)

func TestResolveLicenseInput(t *testing.T) {
	tests := []struct {
		name         string
		licenseValue string
		licenseFile  string
		clear        bool
		want         string
		wantErr      bool
	}{
		{name: "license value", licenseValue: "abc", want: "abc"},
		{name: "clear", clear: true, want: ""},
		{name: "none set", wantErr: true},
		{name: "value and clear", licenseValue: "abc", clear: true, wantErr: true},
		{name: "value and file", licenseValue: "abc", licenseFile: "x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveLicenseInput(tt.licenseValue, tt.licenseFile, tt.clear)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatLicenseInfoShowsOnlyPresentClaims(t *testing.T) {
	users := 500
	claims := &license.Claims{MaxUsers: &users, DeploymentID: "dep-1"}

	out := formatLicenseInfo(claims)

	for _, want := range []string{"Max users:", "500", "Deployment ID:", "dep-1", "Deploy link:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{"Max teams:", "Trial:", "Flags:", "Expiry:"} {
		if strings.Contains(out, absent) {
			t.Errorf("output unexpectedly contains %q:\n%s", absent, out)
		}
	}
}
