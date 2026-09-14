package appadmin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxSamples bounds the dry-run preview. Enough to eyeball the match, not enough
// to dump an entire user table into an HTTP response.
const maxSamples = 20

// ValidateDomain rejects the shapes that would make the LIKE pattern dangerous
// or the result nonsense, and RETURNS the normalized domain. Exported so the
// handler can 400 before connecting.
//
// Returning the normalized value is the point: trimming only a local copy left
// callers free to build the LIKE pattern and the dry-run token from the
// untrimmed input, so " example.com" passed validation and then matched on
// "%@ example.com". Callers must use what comes back, not what they passed in.
//
// The character check is an allowlist. The old blocklist ("%_ \t@") covered
// space and tab but not newline, carriage return, or any other control
// character, so a domain carrying them validated and reached both the token and
// the SQL unchanged.
func ValidateDomain(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", errors.New("domain must not be empty")
	}
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			continue
		}
		// % and _ are LIKE wildcards, @ means a full address was pasted, and
		// anything else (whitespace, control characters, unicode) has no place in
		// a bare domain — punycode is ASCII.
		return "", fmt.Errorf("domain %q must be a bare domain like example.com — letters, digits, dots and hyphens only, with no @, whitespace, or wildcards", d)
	}
	if !strings.Contains(d, ".") {
		return "", fmt.Errorf("domain %q does not look like a domain (no dot)", d)
	}
	if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || strings.Contains(d, "..") {
		return "", fmt.Errorf("domain %q has an empty label", d)
	}
	if strings.HasPrefix(d, "-") || strings.HasSuffix(d, "-") {
		return "", fmt.Errorf("domain %q must not start or end with a hyphen", d)
	}
	return d, nil
}

// domainPattern builds the LIKE pattern for "address in this domain".
//
// The leading "@" matters: console matched "%"+domain, which also matches
// notexample.com when migrating example.com — silently rewriting unrelated
// accounts. Anchoring on the @ makes the match exactly the domain.
func domainPattern(domain string) string {
	return "%@" + domain
}

// MigrateEmailDomain rewrites every account's address from oldDomain to
// newDomain across the four tables that carry it, in one transaction.
func MigrateEmailDomain(
	ctx context.Context,
	db *sql.DB,
	oldDomain, newDomain string,
	dryRun bool,
	confirm func(matched int, fingerprint string) error,
) (matched, updated int, samples []string, fingerprint string, err error) {
	oldDomain, err = ValidateDomain(oldDomain)
	if err != nil {
		return 0, 0, nil, "", err
	}
	newDomain, err = ValidateDomain(newDomain)
	if err != nil {
		return 0, 0, nil, "", err
	}
	if strings.EqualFold(oldDomain, newDomain) {
		return 0, 0, nil, "", errors.New("old and new domain are the same")
	}

	oldPattern := domainPattern(oldDomain)
	newSuffix := "@" + newDomain

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, nil, "", fmt.Errorf("begin transaction: %w", err)
	}
	// Rolled back on any error, and always on a dry run.
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT email FROM users WHERE email LIKE ? ORDER BY email;`, oldPattern)
	if err != nil {
		return 0, 0, nil, "", fmt.Errorf("scan affected users: %w", err)
	}
	digest := sha256.New()
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			_ = rows.Close()
			return 0, 0, nil, "", fmt.Errorf("scan affected user: %w", err)
		}
		matched++
		if len(samples) < maxSamples {
			samples = append(samples, email)
		}
		// Length-prefixed so {"a","bc"} cannot hash the same as {"ab","c"}.
		_, _ = fmt.Fprintf(digest, "%d:%s\n", len(email), email)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, nil, "", fmt.Errorf("read affected users: %w", err)
	}
	_ = rows.Close()
	fingerprint = hex.EncodeToString(digest.Sum(nil))

	if dryRun || matched == 0 {
		return matched, 0, samples, fingerprint, nil
	}

	// Last gate before the first write. The exact matching set is now known and
	// the transaction is open, so a stale or absent preview is rejected against
	// the live accounts rather than whatever the caller claimed.
	if confirm != nil {
		if err := confirm(matched, fingerprint); err != nil {
			return matched, 0, samples, fingerprint, err
		}
	}

	// The `emails` table is keyed by address, so the rewritten rows are inserted
	// and the originals deleted rather than updated in place. ON DUPLICATE KEY
	// makes a re-run idempotent if a target address somehow already exists.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO emails (email, created_at, type, verified)
		 SELECT CONCAT(SUBSTRING_INDEX(email, '@', 1), ?), ?, COALESCE(type, 'PERSONAL'), COALESCE(verified, 0)
		 FROM emails WHERE email LIKE ?
		 ON DUPLICATE KEY UPDATE type = VALUES(type), verified = VALUES(verified);`,
		newSuffix, time.Now().UTC(), oldPattern,
	); err != nil {
		return 0, 0, nil, "", fmt.Errorf("insert rewritten emails: %w", err)
	}

	// Each remaining table stores the address (or auth id) inline, so a
	// CONCAT/SUBSTRING_INDEX rewrite preserves the local part.
	for _, stmt := range []struct{ desc, query string }{
		{"user_emails", `UPDATE user_emails SET email = CONCAT(SUBSTRING_INDEX(email, '@', 1), ?) WHERE email LIKE ?;`},
		{"users.email", `UPDATE users SET email = CONCAT(SUBSTRING_INDEX(email, '@', 1), ?) WHERE email LIKE ?;`},
		{"users.auth_id", `UPDATE users SET auth_id = CONCAT(SUBSTRING_INDEX(auth_id, '@', 1), ?) WHERE auth_id LIKE ?;`},
		{"users_auth0.auth_id", `UPDATE users_auth0 SET auth_id = CONCAT(SUBSTRING_INDEX(auth_id, '@', 1), ?) WHERE auth_id LIKE ?;`},
	} {
		res, execErr := tx.ExecContext(ctx, stmt.query, newSuffix, oldPattern)
		if execErr != nil {
			return 0, 0, nil, "", fmt.Errorf("rewrite %s: %w", stmt.desc, execErr)
		}
		if stmt.desc == "users.email" {
			if n, aErr := res.RowsAffected(); aErr == nil {
				updated = int(n)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM emails WHERE email LIKE ?;`, oldPattern); err != nil {
		return 0, 0, nil, "", fmt.Errorf("remove old-domain emails: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, nil, "", fmt.Errorf("commit migration: %w", err)
	}
	return matched, updated, samples, fingerprint, nil
}
