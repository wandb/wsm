package appadmin

// The app-side contract this file depends on.
//
// Every query and mutation below is defined by gorilla's GraphQL schema, which
// lives in the `core` repo at services/gorilla/schema.graphql. When one of these
// calls starts failing for no apparent reason, that file is the place to check
// first — an argument rename or a removed field shows up here as an unhelpful
// "unknown field" or, worse, as a silently empty result.
//
// The exact definitions, as of writing:
//
//	users(after: String, before: String, first: Int, ids: [ID!], last: Int,
//	      query: String, queryEmail: String, usernames: [String!]): UserConnection
//	                                              schema.graphql:5820
//	viewer(entityName: String): User              schema.graphql:5823
//	updateUser(input: UpdateUserInput!): UpdateUserPayload @audit
//	                                              schema.graphql:5184
//	input UpdateUserInput { admin: Boolean, ... } schema.graphql:2515
//
// Two properties worth knowing because they are not obvious from the calls:
//
//   - `users` exposes queryEmail and usernames as SEPARATE arguments. Querying
//     the wrong one returns an empty connection rather than an error, so a
//     mis-chosen argument looks exactly like "no such user". FindUser picks based
//     on whether the identifier contains "@".
//   - updateUser carries @audit, so every grant and revoke is recorded on the app
//     side. That is the audit trail for this action — nothing here duplicates it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// User is an account as the app reports it.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Admin    *bool  `json:"admin"`
}

// IsAdmin reports the account's admin flag, treating an absent flag as false.
func (u User) IsAdmin() bool { return u.Admin != nil && *u.Admin }

type userLookup struct {
	Users struct {
		Edges []struct {
			Node User `json:"node"`
		} `json:"edges"`
	} `json:"users"`
}

// FindUser resolves an email address or username to an account.
//
// An identifier containing "@" is looked up by email, otherwise by username —
// the app exposes those as different arguments, and querying the wrong one
// silently returns no match rather than an error.
func FindUser(ctx context.Context, addr string, creds Creds, identifier string) (User, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return User{}, errors.New("enter an email address or username")
	}

	query := `query($v: String!) { users(usernames: [$v], first: 1) { edges { node { id username email admin } } } }`
	if strings.Contains(identifier, "@") {
		query = `query($v: String!) { users(queryEmail: $v, first: 1) { edges { node { id username email admin } } } }`
	}

	var found userLookup
	if err := GraphQL(ctx, addr, creds, query, map[string]any{"v": identifier}, &found); err != nil {
		return User{}, err
	}
	// Deliberately generic, and deliberately not echoing the identifier: this
	// package also backs Watchtower's web UI, where a message that distinguishes
	// "no such account" from any other failure is a lookup oracle.
	if len(found.Users.Edges) == 0 {
		return User{}, errors.New("user not found")
	}
	return found.Users.Edges[0].Node, nil
}

// Viewer returns the account the credentials belong to, or an error when they
// authenticate no one.
//
// This is how a caller proves a W&B identity before doing something the app
// itself will not authorize — see RequireAdmin.
func Viewer(ctx context.Context, addr string, creds Creds) (User, error) {
	if !creds.HasIdentity() {
		return User{}, errors.New("no W&B credentials: sign in with a W&B account or supply an API key")
	}
	var out struct {
		Viewer *User `json:"viewer"`
	}
	if err := GraphQL(ctx, addr, creds, `{ viewer { username admin } }`, nil, &out); err != nil {
		return User{}, fmt.Errorf("verify your W&B identity: %w", err)
	}
	if out.Viewer == nil {
		return User{}, errors.New("these credentials do not identify a W&B user")
	}
	return *out.Viewer, nil
}

// RequireAdmin proves the caller is an instance admin, returning their username.
//
// SetAdmin does not need this: it forwards the caller's credentials to a
// mutation the app itself authorizes. MigrateEmailDomain does — it never touches
// the app, it writes to MySQL with a service credential, so without this a
// caller who merely LOOKS credentialed (a UI session cookie that authenticates
// to the UI but not to the app) could trigger irreversible account rewrites with
// no W&B identity behind them.
func RequireAdmin(ctx context.Context, addr string, creds Creds) (string, error) {
	viewer, err := Viewer(ctx, addr, creds)
	if err != nil {
		return "", err
	}
	if !viewer.IsAdmin() {
		return "", fmt.Errorf("%s is not a W&B instance admin", viewer.Username)
	}
	return viewer.Username, nil
}

// SetAdmin grants or revokes instance-admin for one account and returns it as
// the app reports it afterwards, along with whether the flag actually moved.
//
// The mutation carries the CALLER's credentials, so the app enforces the real
// rules — a non-admin cannot set the flag, and an admin cannot revoke their own —
// and runs its own side effects (team-role propagation, billing). Its refusal
// message is returned verbatim, since it explains the rule that was hit better
// than any restatement here.
//
// `changed` is reported rather than inferred because the mutation is idempotent:
// granting admin to an existing admin succeeds and looks identical to a real
// promotion, which is misleading to report as one.
func SetAdmin(ctx context.Context, addr string, creds Creds, identifier string, admin bool) (user User, changed bool, err error) {
	if !creds.HasIdentity() {
		return User{}, false, errors.New("no W&B credentials: changing admin status is authorized as the caller, so a W&B account or API key is required")
	}

	// Look the account up first: the mutation needs the app's user ID, and
	// echoing the resolved account back lets a caller confirm the right one
	// changed rather than trusting an identifier match.
	target, err := FindUser(ctx, addr, creds, identifier)
	if err != nil {
		return User{}, false, err
	}

	const mutation = `mutation($id: ID!, $admin: Boolean!) {
		updateUser(input: {id: $id, admin: $admin}) { user { id username admin } }
	}`
	var updated struct {
		UpdateUser *struct {
			User *User `json:"user"`
		} `json:"updateUser"`
	}
	if err := GraphQL(ctx, addr, creds, mutation,
		map[string]any{"id": target.ID, "admin": admin}, &updated); err != nil {
		return User{}, false, err
	}

	// A null user with no `errors` entry is a real response shape — a resolver
	// that failed after passing validation produces it. Filling Admin in from the
	// REQUEST turned that into apparent success, so the CLI reported a change the
	// app never made. Require the app to state the outcome instead.
	if updated.UpdateUser == nil || updated.UpdateUser.User == nil {
		return User{}, false, errors.New("the W&B app returned no updated user; the admin flag may not have changed — re-run to check")
	}
	result := *updated.UpdateUser.User
	if result.ID == "" || result.Admin == nil {
		return User{}, false, errors.New("the W&B app returned an incomplete user; the admin flag may not have changed — re-run to check")
	}
	// email is the one field the mutation does not select, so it is the only one
	// backfilled from the lookup.
	result.Email = target.Email
	if result.Username == "" {
		result.Username = target.Username
	}
	return result, target.IsAdmin() != result.IsAdmin(), nil
}
