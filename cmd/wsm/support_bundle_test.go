package main

import "testing"

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"kind-wsm-sb":             "kind-wsm-sb",
		"arn:aws:eks:us-east-1/x": "arn:aws:eks:us-east-1/x",
		"prod east":               "'prod east'",
		"it's":                    `'it'\''s'`,
		"":                        "''",
	}
	for in, want := range tests {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
