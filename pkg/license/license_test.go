package license

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func makeToken(t *testing.T, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	seg := base64.RawURLEncoding.EncodeToString(body)
	return "header." + seg + ".signature"
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  error
	}{
		{"empty", "", ErrNoLicense},
		{"whitespace", "   ", ErrNoLicense},
		{"one segment", "abc", ErrMalformed},
		{"two segments", "abc.def", ErrMalformed},
		{"four segments", "a.b.c.d", ErrMalformed},
		{"non-base64 payload", "header.!!!.sig", ErrMalformed},
		{"non-json payload", "header." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig", ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.token)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decode(%q) error = %v, want %v", tt.token, err, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := makeToken(t, map[string]any{"maxTeams": 1})
	tests := []struct {
		name  string
		token string
		want  error
	}{
		{"valid", valid, nil},
		{"empty", "", ErrNoLicense},
		{"whitespace", "  ", ErrNoLicense},
		{"two segments", "a.b", ErrMalformed},
		{"empty header", ".payload.sig", ErrMalformed},
		{"empty payload", "header..sig", ErrMalformed},
		{"empty signature", "header.payload.", ErrMalformed},
		{"non-json payload", "header." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".sig", ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.token)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate(%q) = %v, want nil", tt.token, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate(%q) error = %v, want %v", tt.token, err, tt.want)
			}
		})
	}
}

func TestDecodeResolvedViews(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		check   func(t *testing.T, c *Claims)
	}{
		{
			name: "primary fields preferred over aliases",
			payload: map[string]any{
				"maxUsers":         500,
				"seats":            1,
				"maxViewOnlyUsers": 15,
				"viewOnlySeats":    1,
				"maxTeams":         10,
				"teams":            1,
			},
			check: func(t *testing.T, c *Claims) {
				assertIntOK(t, "Users", c.Users, 500)
				assertIntOK(t, "ViewOnlyUsers", c.ViewOnlyUsers, 15)
				assertIntOK(t, "MaxTeamsResolved", c.MaxTeamsResolved, 10)
			},
		},
		{
			name: "alias fallback",
			payload: map[string]any{
				"seats":         500,
				"viewOnlySeats": 15,
				"teams":         10,
			},
			check: func(t *testing.T, c *Claims) {
				assertIntOK(t, "Users", c.Users, 500)
				assertIntOK(t, "ViewOnlyUsers", c.ViewOnlyUsers, 15)
				assertIntOK(t, "MaxTeamsResolved", c.MaxTeamsResolved, 10)
			},
		},
		{
			name:    "absent fields report not present",
			payload: map[string]any{},
			check: func(t *testing.T, c *Claims) {
				if _, ok := c.Users(); ok {
					t.Error("Users() ok = true, want false")
				}
				if _, ok := c.ViewOnlyUsers(); ok {
					t.Error("ViewOnlyUsers() ok = true, want false")
				}
				if _, ok := c.MaxTeamsResolved(); ok {
					t.Error("MaxTeamsResolved() ok = true, want false")
				}
				if _, ok := c.ExpiresAt(); ok {
					t.Error("ExpiresAt() ok = true, want false")
				}
			},
		},
		{
			name: "unknown claims retained in Raw and non-fatal",
			payload: map[string]any{
				"maxUsers":     10,
				"weaveLimits":  map[string]any{"weaveOverageUnit": "MB"},
				"someNewClaim": "future",
			},
			check: func(t *testing.T, c *Claims) {
				assertIntOK(t, "Users", c.Users, 10)
				if _, ok := c.Raw["weaveLimits"]; !ok {
					t.Error("Raw missing weaveLimits")
				}
				if got := c.Raw["someNewClaim"]; got != "future" {
					t.Errorf("Raw[someNewClaim] = %v, want future", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Decode(makeToken(t, tt.payload))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			tt.check(t, c)
		})
	}
}

func TestExpiresAt(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    time.Time
	}{
		{
			name:    "prefers expiresAt over exp",
			payload: map[string]any{"expiresAt": "2050-08-22T04:59:59Z", "exp": 1700000000},
			want:    time.Date(2050, 8, 22, 4, 59, 59, 0, time.UTC),
		},
		{
			name:    "falls back to exp",
			payload: map[string]any{"exp": 2544757199},
			want:    time.Unix(2544757199, 0).UTC(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Decode(makeToken(t, tt.payload))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			got, ok := c.ExpiresAt()
			if !ok {
				t.Fatal("ExpiresAt() ok = false, want true")
			}
			if !got.Equal(tt.want) {
				t.Errorf("ExpiresAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExpired(t *testing.T) {
	past := Claims{ExpiresAtRaw: "2000-01-01T00:00:00Z"}
	if !past.Expired() {
		t.Error("past license Expired() = false, want true")
	}
	future := Claims{ExpiresAtRaw: "2100-01-01T00:00:00Z"}
	if future.Expired() {
		t.Error("future license Expired() = true, want false")
	}
	unknown := Claims{}
	if unknown.Expired() {
		t.Error("unknown expiry Expired() = true, want false")
	}
}

func TestLink(t *testing.T) {
	withID := Claims{DeploymentID: "abc-123"}
	if got, want := withID.Link(), "https://deploy.wandb.ai/abc-123"; got != want {
		t.Errorf("Link() = %q, want %q", got, want)
	}
	noID := Claims{}
	if got := noID.Link(); got != "" {
		t.Errorf("Link() = %q, want empty", got)
	}
}

func TestDecodeToleratesPadding(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"maxTeams": 3})
	padded := base64.URLEncoding.EncodeToString(body)
	c, err := Decode("header." + padded + ".sig")
	if err != nil {
		t.Fatalf("Decode padded: %v", err)
	}
	assertIntOK(t, "MaxTeamsResolved", c.MaxTeamsResolved, 3)
}

func assertIntOK(t *testing.T, name string, fn func() (int, bool), want int) {
	t.Helper()
	got, ok := fn()
	if !ok {
		t.Fatalf("%s() ok = false, want true", name)
	}
	if got != want {
		t.Errorf("%s() = %d, want %d", name, got, want)
	}
}
