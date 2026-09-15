package appadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// graphqlPath is the app's GraphQL endpoint, served by gorilla at the same
// host:port that validates sessions.
const graphqlPath = "/graphql"

// RequestTimeout bounds a single call to the app.
const RequestTimeout = 20 * time.Second

// Creds are the credentials the caller presents, forwarded to the app so it
// authorizes the action as that user. Nothing in this package holds an elevated
// credential of its own for the app.
//
// Either arm authenticates:
//
//   - Authorization: an API key. The CLI uses this, and it is not subject to the
//     CSRF check below. Nothing here persists it: it is read from --api-key or
//     $WANDB_API_KEY at invocation and lives only in this process. Prefer the env
//     var — a flag value lands in shell history and in `ps` output — and when
//     running as an in-cluster Job, project it from a Secret via env.valueFrom
//     rather than baking it into the pod spec.
//   - Cookie: a browser session. A UI forwarding a user's session uses this.
//
// Origin and Referer matter and are easy to miss. Gorilla's session middleware
// refuses a COOKIE-authenticated request whose Origin is not an allowed one —
// its CSRF defence — and answers "Request origin not allowed" rather than
// anything about the query. Forward them from the incoming request when
// relaying a browser session.
//
// They are forwarded, never synthesized: manufacturing an Origin would hand
// every cookie-bearing request the exemption the check exists to withhold.
type Creds struct {
	Cookie        string
	Authorization string
	Origin        string
	Referer       string
}

// HasIdentity reports whether the caller presented anything the app could
// accept. It says nothing about WHO they are — only that there is something to
// authorize with, so a caller with neither can be told plainly instead of
// receiving the app's "not logged in" for an action it cannot attempt.
//
// Origin and Referer are excluded: they gate a request but prove nothing about
// who is making it. A UI relaying its own session cookie should strip that
// cookie before building Creds, or a password-only session reads as credentialed.
func (c Creds) HasIdentity() bool { return c.Cookie != "" || c.Authorization != "" }

type graphqlError struct {
	Message string `json:"message"`
}

type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlError  `json:"errors"`
}

// BaseURL turns an app address into the base URL of its GraphQL endpoint.
//
// A bare host:port keeps the http:// default, which is what an in-cluster
// Service address is. Anything reached from outside the cluster — a
// port-forward, an ingress — should carry an explicit https:// prefix, because
// the API key and any session cookie cross that hop in the clear otherwise.
// Exported so a caller can reject a bad address before it holds a credential.
func BaseURL(addr string) (string, error) {
	addr = strings.TrimSuffix(strings.TrimSpace(addr), "/")
	if addr == "" {
		return "", errors.New("no W&B app address: pass the host:port serving the app's GraphQL endpoint")
	}
	switch {
	case strings.HasPrefix(addr, "https://"), strings.HasPrefix(addr, "http://"):
		return addr, nil
	case strings.Contains(addr, "://"):
		return "", fmt.Errorf("app address %q: only http:// and https:// are supported", addr)
	default:
		return "http://" + addr, nil
	}
}

// GraphQL posts a query to the app at addr (a bare host:port, e.g. "api:8080",
// or a scheme-qualified base URL — see BaseURL), forwarding creds, and
// unmarshals `data` into out. Pass a nil out to discard it.
//
// GraphQL errors come back as Go errors: the app answers HTTP 200 even when the
// operation was refused, so a caller checking only the status would treat a
// rejection as success.
func GraphQL(ctx context.Context, addr string, creds Creds, query string, variables map[string]any, out any) error {
	base, err := BaseURL(addr)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+graphqlPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if creds.Cookie != "" {
		req.Header.Set("Cookie", creds.Cookie)
	}
	if creds.Authorization != "" {
		req.Header.Set("Authorization", creds.Authorization)
	}
	// Required for a cookie session to be accepted at all — see Creds.
	if creds.Origin != "" {
		req.Header.Set("Origin", creds.Origin)
	}
	if creds.Referer != "" {
		req.Header.Set("Referer", creds.Referer)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach the W&B app at %s: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("W&B app returned HTTP %d", resp.StatusCode)
	}

	var parsed graphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("decode W&B app response: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return errors.New(parsed.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(parsed.Data, out)
}
