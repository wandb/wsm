package license

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const deployBaseURL = "https://deploy.wandb.ai"

// ErrNoLicense is a distinct "no license set" state, not a decoding failure.
var ErrNoLicense = errors.New("license: No License Set")

var ErrMalformed = errors.New("license: Malformed Token")

// Claims holds the license payload
type Claims struct {
	ExpiresAtRaw     string   `json:"expiresAt,omitempty"`
	Exp              *int64   `json:"exp,omitempty"`
	MaxTeams         *int     `json:"maxTeams,omitempty"`
	Teams            *int     `json:"teams,omitempty"`
	MaxUsers         *int     `json:"maxUsers,omitempty"`
	Seats            *int     `json:"seats,omitempty"`
	MaxViewOnlyUsers *int     `json:"maxViewOnlyUsers,omitempty"`
	ViewOnlySeats    *int     `json:"viewOnlySeats,omitempty"`
	DeploymentID     string   `json:"deploymentId,omitempty"`
	Trial            *bool    `json:"trial,omitempty"`
	Flags            []string `json:"flags,omitempty"`

	Raw map[string]any `json:"-"`
}

// Validate reports whether token is a usable license
func Validate(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrNoLicense
	}

	segments := strings.Split(token, ".")
	if len(segments) != 3 || segments[0] == "" || segments[1] == "" || segments[2] == "" {
		return fmt.Errorf("%w: expected a JWT with three non-empty segments", ErrMalformed)
	}

	payload, err := decodeSegment(segments[1])
	if err != nil {
		return fmt.Errorf("%w: payload is not base64url: %v", ErrMalformed, err)
	}
	if !json.Valid(payload) {
		return fmt.Errorf("%w: payload is not JSON", ErrMalformed)
	}

	return nil
}

// Decode validates the token then parses its payload into claims.
func Decode(token string) (*Claims, error) {
	if err := Validate(token); err != nil {
		return nil, err
	}

	payload, err := decodeSegment(strings.Split(strings.TrimSpace(token), ".")[1])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	claims := &Claims{}
	if err := json.Unmarshal(payload, claims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if err := json.Unmarshal(payload, &claims.Raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	return claims, nil
}

func decodeSegment(segment string) ([]byte, error) {
	if data, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		return data, nil
	}
	return base64.URLEncoding.DecodeString(segment)
}

// ExpiresAt prefers the expiresAt claim and falls back to exp.
func (c *Claims) ExpiresAt() (time.Time, bool) {
	if c.ExpiresAtRaw != "" {
		if t, err := time.Parse(time.RFC3339, c.ExpiresAtRaw); err == nil {
			return t, true
		}
	}
	if c.Exp != nil {
		return time.Unix(*c.Exp, 0).UTC(), true
	}
	return time.Time{}, false
}

// Users prefers maxUsers and falls back to the seats alias.
func (c *Claims) Users() (int, bool) {
	if c.MaxUsers != nil {
		return *c.MaxUsers, true
	}
	if c.Seats != nil {
		return *c.Seats, true
	}
	return 0, false
}

// ViewOnlyUsers prefers maxViewOnlyUsers and falls back to the viewOnlySeats alias.
func (c *Claims) ViewOnlyUsers() (int, bool) {
	if c.MaxViewOnlyUsers != nil {
		return *c.MaxViewOnlyUsers, true
	}
	if c.ViewOnlySeats != nil {
		return *c.ViewOnlySeats, true
	}
	return 0, false
}

// MaxTeamsResolved prefers maxTeams and falls back to the teams alias.
func (c *Claims) MaxTeamsResolved() (int, bool) {
	if c.MaxTeams != nil {
		return *c.MaxTeams, true
	}
	if c.Teams != nil {
		return *c.Teams, true
	}
	return 0, false
}

// Expired reports whether the license is past expiry, or false when unknown.
func (c *Claims) Expired() bool {
	expiry, ok := c.ExpiresAt()
	if !ok {
		return false
	}
	return time.Now().After(expiry)
}

// Link returns the deploy.wandb.ai URL, or "" when there is no deployment id.
func (c *Claims) Link() string {
	if c.DeploymentID == "" {
		return ""
	}
	return deployBaseURL + "/" + c.DeploymentID
}
