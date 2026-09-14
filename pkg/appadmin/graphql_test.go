package appadmin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubApp answers one GraphQL call with the given body and records the request
// it saw, so the assertions below can be about headers rather than plumbing.
func stubApp(t *testing.T, body string) (addr string, seen *http.Request, query *string) {
	t.Helper()
	var captured http.Request
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = *r.Clone(r.Context())
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &in)
		capturedQuery = in.Query
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &captured, &capturedQuery
}

// Gorilla's session middleware refuses a COOKIE-authenticated request whose
// Origin is not an allowed one — its CSRF defence — and answers "Request origin
// not allowed" rather than anything about the query. Forwarding the cookie
// without Origin therefore failed for reasons that looked like a broken query.
func TestGraphQLForwardsCredentialsAndOrigin(t *testing.T) {
	addr, seen, _ := stubApp(t, `{"data":{"viewer":{"username":"admin","admin":true}}}`)

	creds := Creds{
		Cookie:        "wandb=session",
		Authorization: "Bearer key",
		Origin:        "https://wandb.example.com",
		Referer:       "https://wandb.example.com/console",
	}
	if err := GraphQL(context.Background(), addr, creds, `{ viewer { username } }`, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for header, want := range map[string]string{
		"Cookie":        "wandb=session",
		"Authorization": "Bearer key",
		"Origin":        "https://wandb.example.com",
		"Referer":       "https://wandb.example.com/console",
		"Content-Type":  "application/json",
	} {
		if got := seen.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// Absent credentials must stay absent rather than being sent empty, and an
// Origin must never be manufactured — that would hand every cookie-bearing
// request the CSRF exemption the check exists to withhold.
func TestGraphQLDoesNotInventHeaders(t *testing.T) {
	addr, seen, _ := stubApp(t, `{"data":{}}`)
	if err := GraphQL(context.Background(), addr, Creds{}, `{ viewer { username } }`, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, h := range []string{"Cookie", "Authorization", "Origin", "Referer"} {
		if got := seen.Header.Get(h); got != "" {
			t.Errorf("%s was sent as %q with no credentials supplied", h, got)
		}
	}
}

// The app answers HTTP 200 even when it refuses the operation, so a caller
// checking only the status would read a rejection as success.
func TestGraphQLSurfacesErrorsFromA200(t *testing.T) {
	addr, _, _ := stubApp(t, `{"errors":[{"message":"user is not authorized"}],"data":null}`)
	err := GraphQL(context.Background(), addr, Creds{Authorization: "k"}, `{ viewer { username } }`, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("got %v, want the app's own refusal message", err)
	}
}

func TestGraphQLRequiresAnAddress(t *testing.T) {
	err := GraphQL(context.Background(), "", Creds{}, `{}`, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no W&B app address") {
		t.Fatalf("got %v, want a clear missing-address error", err)
	}
}

// Origin and Referer gate a request but prove nothing about who is making it, so
// they must not count as identity. A UI relaying only its OWN session cookie
// would otherwise read as credentialed.
func TestHasIdentityIgnoresOriginAndReferer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds Creds
		want  bool
	}{
		{"nothing", Creds{}, false},
		{"origin only", Creds{Origin: "https://x", Referer: "https://x/y"}, false},
		{"cookie", Creds{Cookie: "wandb=s"}, true},
		{"api key", Creds{Authorization: "Bearer k"}, true},
	} {
		if got := tc.creds.HasIdentity(); got != tc.want {
			t.Errorf("%s: HasIdentity() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An identifier containing "@" is an email and anything else a username. The app
// exposes those as different arguments, and querying the wrong one returns no
// match rather than an error — so the choice has to be right.
func TestFindUserPicksTheRightLookupArgument(t *testing.T) {
	addr, _, query := stubApp(t, `{"data":{"users":{"edges":[{"node":{"id":"1","username":"u","email":"u@x.com"}}]}}}`)

	if _, err := FindUser(context.Background(), addr, Creds{Authorization: "k"}, "u@x.com"); err != nil {
		t.Fatalf("email lookup: %v", err)
	}
	if !strings.Contains(*query, "queryEmail") {
		t.Errorf("an address should be looked up by queryEmail, got %q", *query)
	}

	if _, err := FindUser(context.Background(), addr, Creds{Authorization: "k"}, "someuser"); err != nil {
		t.Fatalf("username lookup: %v", err)
	}
	if !strings.Contains(*query, "usernames") {
		t.Errorf("a username should be looked up by usernames, got %q", *query)
	}
}

func TestFindUserReportsNoMatch(t *testing.T) {
	addr, _, _ := stubApp(t, `{"data":{"users":{"edges":[]}}}`)
	_, err := FindUser(context.Background(), addr, Creds{Authorization: "k"}, "ghost@x.com")
	if err == nil || !strings.Contains(err.Error(), "no W&B user matches") {
		t.Fatalf("got %v, want a no-match error", err)
	}
}

// RequireAdmin exists because MigrateEmailDomain writes with a service
// credential and never consults the app. Without it, a caller who merely LOOKS
// credentialed could trigger an irreversible rewrite with no W&B identity.
func TestRequireAdmin(t *testing.T) {
	t.Run("admin passes", func(t *testing.T) {
		addr, _, _ := stubApp(t, `{"data":{"viewer":{"username":"boss","admin":true}}}`)
		who, err := RequireAdmin(context.Background(), addr, Creds{Authorization: "k"})
		if err != nil || who != "boss" {
			t.Fatalf("got %q / %v, want boss / nil", who, err)
		}
	})

	t.Run("non-admin refused by name", func(t *testing.T) {
		addr, _, _ := stubApp(t, `{"data":{"viewer":{"username":"regular","admin":false}}}`)
		_, err := RequireAdmin(context.Background(), addr, Creds{Authorization: "k"})
		if err == nil || !strings.Contains(err.Error(), "regular is not a W&B instance admin") {
			t.Fatalf("got %v, want a named refusal", err)
		}
	})

	// A null viewer is the shape a credential that authenticates nobody produces —
	// a UI session cookie that is valid for the UI but means nothing to the app.
	t.Run("null viewer refused", func(t *testing.T) {
		addr, _, _ := stubApp(t, `{"data":{"viewer":null}}`)
		_, err := RequireAdmin(context.Background(), addr, Creds{Cookie: "looks-real"})
		if err == nil || !strings.Contains(err.Error(), "do not identify a W&B user") {
			t.Fatalf("got %v, want a no-identity error", err)
		}
	})

	t.Run("no credentials refused without calling the app", func(t *testing.T) {
		_, err := RequireAdmin(context.Background(), "unused:1", Creds{})
		if err == nil || !strings.Contains(err.Error(), "no W&B credentials") {
			t.Fatalf("got %v, want a missing-credentials error", err)
		}
	})
}

// The mutation omits email, so the resolved account fills it in — otherwise the
// caller cannot confirm WHICH account changed.
func TestSetAdminFillsGapsFromTheResolvedAccount(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = io.WriteString(w, `{"data":{"users":{"edges":[{"node":{"id":"42","username":"target","email":"target@x.com","admin":false}}]}}}`)
			return
		}
		// updateUser returns id/username/admin — no email.
		_, _ = io.WriteString(w, `{"data":{"updateUser":{"user":{"id":"42","username":"target","admin":true}}}}`)
	}))
	defer srv.Close()

	user, err := SetAdmin(context.Background(), strings.TrimPrefix(srv.URL, "http://"),
		Creds{Authorization: "k"}, "target@x.com", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !user.IsAdmin() {
		t.Error("admin flag was not reported")
	}
	if user.Email != "target@x.com" {
		t.Errorf("email = %q, want it carried over from the lookup", user.Email)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want a lookup then a mutation", calls)
	}
}

// The app enforces the real rules (a non-admin cannot set the flag, an admin
// cannot revoke their own), so its message is returned verbatim.
func TestSetAdminSurfacesTheAppsRefusal(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = io.WriteString(w, `{"data":{"users":{"edges":[{"node":{"id":"42","username":"target"}}]}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"errors":[{"message":"cannot remove your own admin status"}]}`)
	}))
	defer srv.Close()

	_, err := SetAdmin(context.Background(), strings.TrimPrefix(srv.URL, "http://"),
		Creds{Authorization: "k"}, "target", false)
	if err == nil || !strings.Contains(err.Error(), "cannot remove your own admin status") {
		t.Fatalf("got %v, want the app's message verbatim", err)
	}
}

func TestSetAdminRequiresCredentials(t *testing.T) {
	_, err := SetAdmin(context.Background(), "unused:1", Creds{}, "someone", true)
	if err == nil || !strings.Contains(err.Error(), "no W&B credentials") {
		t.Fatalf("got %v, want a missing-credentials error", err)
	}
}
