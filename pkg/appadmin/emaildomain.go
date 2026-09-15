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

// maxDomainLen and maxLabelLen are the DNS limits. A domain past them cannot
// resolve, so an address built on it is dead on arrival.
const (
	maxDomainLen = 253
	maxLabelLen  = 63
)

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
	if len(d) > maxDomainLen {
		return "", fmt.Errorf("domain %q is longer than the %d-character DNS limit", d, maxDomainLen)
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

	// Validate EVERY label, not just the domain's outer boundaries. Checking
	// HasPrefix/HasSuffix on the whole string accepted "good.-invalid.com",
	// whose middle label is not a legal DNS label, and then used it as the new
	// address suffix.
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("domain %q does not look like a domain (no dot)", d)
	}
	for _, label := range labels {
		switch {
		case label == "":
			return "", fmt.Errorf("domain %q has an empty label", d)
		case len(label) > maxLabelLen:
			return "", fmt.Errorf("domain %q has a label longer than the %d-character DNS limit", d, maxLabelLen)
		case strings.HasPrefix(label, "-"), strings.HasSuffix(label, "-"):
			return "", fmt.Errorf("domain %q has a label that starts or ends with a hyphen (%q)", d, label)
		}
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

// affectedTables is the complete set of columns a migration rewrites. Both the
// preview and the write derive from this one list, so the numbers an operator
// confirms describe every row that changes.
//
// The preview used to scan only users.email while the write also rewrote
// user_emails, users.auth_id and users_auth0.auth_id. Those sets diverge after a
// partial migration or an alias, so the confirmed count described less than the
// write did — and `matched == 0` returned early, skipping auth rows that still
// carried the old domain.
var affectedTables = []struct{ desc, table, column string }{
	{"emails.email", "emails", "email"},
	{"user_emails.email", "user_emails", "email"},
	{"users.email", "users", "email"},
	{"users.auth_id", "users", "auth_id"},
	{"users_auth0.auth_id", "users_auth0", "auth_id"},
}

// Plan is everything a migration would rewrite, as observed in one transaction.
type Plan struct {
	// Accounts is distinct users.email matches — the "how many people" number.
	Accounts int
	// Rows is the total across every affected column. It exceeds Accounts
	// whenever an account carries aliases or an auth id, so both are reported
	// rather than one standing in for the other.
	Rows int
	// Samples are up to maxSamples users.email addresses, for eyeballing.
	Samples []string
	// Fingerprint covers every row in the plan. ApplyEmailDomainMigration will
	// not write unless its own scan produces the same value.
	Fingerprint string
}

// Empty reports whether there is nothing to migrate.
func (p Plan) Empty() bool { return p.Rows == 0 }

// validatePair normalizes both domains and rejects a no-op migration.
func validatePair(oldDomain, newDomain string) (string, string, error) {
	o, err := ValidateDomain(oldDomain)
	if err != nil {
		return "", "", err
	}
	n, err := ValidateDomain(newDomain)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(o, n) {
		return "", "", errors.New("old and new domain are the same")
	}
	return o, n, nil
}

// PlanEmailDomainMigration reports every row a migration would rewrite, without
// writing anything.
func PlanEmailDomainMigration(ctx context.Context, db *sql.DB, oldDomain, newDomain string) (Plan, error) {
	oldDomain, _, err := validatePair(oldDomain, newDomain)
	if err != nil {
		return Plan{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Plan{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return scanPlan(ctx, tx, domainPattern(oldDomain), false)
}

// ApplyEmailDomainMigration performs the rewrite, but only if the live rows
// still match the plan the caller confirmed.
//
// The confirmed plan is a REQUIRED argument rather than an optional callback:
// the previous shape let a caller pass dryRun=false with confirm=nil and get an
// irreversible bulk rewrite with no authorization gate at all.
func ApplyEmailDomainMigration(
	ctx context.Context,
	db *sql.DB,
	oldDomain, newDomain string,
	confirmed Plan,
) (Plan, error) {
	oldDomain, newDomain, err := validatePair(oldDomain, newDomain)
	if err != nil {
		return Plan{}, err
	}
	if confirmed.Fingerprint == "" {
		return Plan{}, errors.New("refusing to migrate without a confirmed plan: call PlanEmailDomainMigration first and pass what it returned")
	}

	oldPattern := domainPattern(oldDomain)
	newSuffix := "@" + newDomain

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, fmt.Errorf("begin transaction: %w", err)
	}
	// Rolled back on any error; the commit below is the only way out.
	defer func() { _ = tx.Rollback() }()

	// Re-scan with FOR UPDATE so the rows about to be rewritten are locked for
	// the rest of the transaction. Without it MySQL evaluates each UPDATE's WHERE
	// against the current read view rather than the scan's snapshot, so a row
	// that started matching after the preview would join the write — and after
	// the fingerprint check had already passed.
	live, err := scanPlan(ctx, tx, oldPattern, true)
	if err != nil {
		return Plan{}, err
	}
	if live.Fingerprint != confirmed.Fingerprint {
		return live, fmt.Errorf(
			"the rows matching @%s changed since the plan was confirmed (%d account(s)/%d row(s) then, %d/%d now); re-run to review the new set",
			oldDomain, confirmed.Accounts, confirmed.Rows, live.Accounts, live.Rows)
	}
	if live.Empty() {
		return live, nil
	}

	// The `emails` table is keyed by address, so rewritten rows are inserted and
	// the originals deleted rather than updated in place. ON DUPLICATE KEY makes
	// a re-run idempotent if a target address somehow already exists.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO emails (email, created_at, type, verified)
		 SELECT CONCAT(SUBSTRING_INDEX(email, '@', 1), ?), ?, COALESCE(type, 'PERSONAL'), COALESCE(verified, 0)
		 FROM emails WHERE email LIKE ?
		 ON DUPLICATE KEY UPDATE type = VALUES(type), verified = VALUES(verified);`,
		newSuffix, time.Now().UTC(), oldPattern,
	); err != nil {
		return live, fmt.Errorf("insert rewritten emails: %w", err)
	}

	// Every remaining column stores the address (or auth id) inline, so a
	// CONCAT/SUBSTRING_INDEX rewrite preserves the local part. These are exactly
	// the columns scanPlan fingerprinted, which is what makes the confirmed
	// numbers describe the whole write.
	for _, t := range affectedTables {
		if t.table == "emails" {
			continue // handled by the insert above and the delete below
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET %s = CONCAT(SUBSTRING_INDEX(%s, '@', 1), ?) WHERE %s LIKE ?;`,
			t.table, t.column, t.column, t.column), newSuffix, oldPattern,
		); err != nil {
			return live, fmt.Errorf("rewrite %s: %w", t.desc, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM emails WHERE email LIKE ?;`, oldPattern); err != nil {
		return live, fmt.Errorf("remove old-domain emails: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return live, fmt.Errorf("commit migration: %w", err)
	}
	return live, nil
}

// scanPlan reads every affected row inside tx. forUpdate locks them for the
// transaction's lifetime.
func scanPlan(ctx context.Context, tx *sql.Tx, oldPattern string, forUpdate bool) (Plan, error) {
	var plan Plan
	digest := sha256.New()

	for _, t := range affectedTables {
		// The identifiers come from the constant list above, never from user
		// input; the pattern stays a real placeholder.
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s LIKE ? ORDER BY %s`,
			t.column, t.table, t.column, t.column)
		if forUpdate {
			query += " FOR UPDATE"
		}
		rows, err := tx.QueryContext(ctx, query+";", oldPattern)
		if err != nil {
			return Plan{}, fmt.Errorf("scan %s: %w", t.desc, err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				return Plan{}, fmt.Errorf("scan %s: %w", t.desc, err)
			}
			plan.Rows++
			// Length-prefixed and column-qualified so {"a","bc"} cannot hash the
			// same as {"ab","c"}, and the same address in two tables counts twice.
			_, _ = fmt.Fprintf(digest, "%s %d:%s\n", t.desc, len(value), value)
			if t.desc == "users.email" {
				plan.Accounts++
				if len(plan.Samples) < maxSamples {
					plan.Samples = append(plan.Samples, value)
				}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return Plan{}, fmt.Errorf("read %s: %w", t.desc, err)
		}
		_ = rows.Close()
	}

	plan.Fingerprint = hex.EncodeToString(digest.Sum(nil))
	return plan, nil
}
