package appadmin

import "testing"

func TestValidateDomain(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
		why     string
	}{
		{"example.com", false, "a bare domain is the expected shape"},
		{"sub.example.co.uk", false, "subdomains are fine"},
		{"my-corp.example.com", false, "hyphens are legal inside labels"},
		{"", true, "empty"},
		{"   ", true, "whitespace only"},
		{"example", true, "no dot — not a domain"},
		{"user@example.com", true, "a full address, not a domain"},
		{"exam%ple.com", true, "% is a LIKE wildcard"},
		{"exam_ple.com", true, "_ is a LIKE wildcard"},
		{"exa mple.com", true, "contains a space"},
		// The blocklist this replaced checked only space and tab, so every one of
		// these validated and then reached the token and the SQL unchanged.
		{"exa\nmple.com", true, "contains a newline"},
		{"exa\rmple.com", true, "contains a carriage return"},
		{"exa\vmple.com", true, "contains a vertical tab"},
		{"exa\x00mple.com", true, "contains a NUL"},
		{"exa\tmple.com", true, "contains a tab"},
		{"exämple.com", true, "non-ASCII — punycode is required"},
		{".example.com", true, "leading dot is an empty label"},
		{"example.com.", true, "trailing dot is an empty label"},
		{"exam..ple.com", true, "doubled dot is an empty label"},
		{"-example.com", true, "leading hyphen"},
		{"example.com-", true, "trailing hyphen"},
	} {
		got, err := ValidateDomain(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateDomain(%q) err=%v, wantErr=%v (%s)", tc.in, err, tc.wantErr, tc.why)
		}
		if err != nil && got != "" {
			t.Errorf("ValidateDomain(%q) returned %q alongside an error", tc.in, got)
		}
	}
}

// The regression: ValidateDomain trimmed a local copy and returned only an
// error, so callers kept using the raw input. " example.com" validated and then
// built the pattern "%@ example.com", matching nothing, while the dry-run token
// was signed over the untrimmed string.
func TestValidateDomainReturnsTheNormalizedDomain(t *testing.T) {
	for _, in := range []string{"example.com", " example.com", "example.com ", "\texample.com\n", "  example.com  "} {
		got, err := ValidateDomain(in)
		if err != nil {
			t.Errorf("ValidateDomain(%q) unexpected error: %v", in, err)
			continue
		}
		if got != "example.com" {
			t.Errorf("ValidateDomain(%q) = %q, want the trimmed %q", in, got, "example.com")
		}
		if p := domainPattern(got); p != "%@example.com" {
			t.Errorf("pattern from %q = %q, want %q", in, p, "%@example.com")
		}
	}
}

// The regression this exists for: console matched "%"+domain, so migrating
// example.com also rewrote notexample.com accounts. Anchoring on "@" is the fix.
func TestDomainPatternAnchorsOnTheAtSign(t *testing.T) {
	got := domainPattern("example.com")
	if got != "%@example.com" {
		t.Fatalf("domainPattern = %q, want %q", got, "%@example.com")
	}
	// Demonstrate the discrimination the anchor buys, using SQL LIKE semantics
	// (% matches any run of characters, so the check is a suffix comparison).
	for _, tc := range []struct {
		email string
		match bool
	}{
		{"someone@example.com", true},
		{"someone@notexample.com", false},
		{"someone@sub.example.com", false},
		{"example.com", false},
	} {
		if likeSuffixMatch(tc.email, got) != tc.match {
			t.Errorf("%q against %q: got %v, want %v", tc.email, got, !tc.match, tc.match)
		}
	}
}

// likeSuffixMatch models `email LIKE '%@domain'` for the assertions above.
func likeSuffixMatch(email, pattern string) bool {
	suffix := pattern[1:] // strip the leading %
	return len(email) > len(suffix) && email[len(email)-len(suffix):] == suffix
}
