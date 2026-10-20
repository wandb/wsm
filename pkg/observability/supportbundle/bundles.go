package supportbundle

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/client-go/kubernetes"
)

// BundleIDMetadata is the object metadata Lumen stamps with the bundle ID.
const BundleIDMetadata = "wandb-support-bundle-id"

// Bundle is one stored archive as reported by bundle-manager list.
type Bundle struct {
	Location     string            `json:"location"`
	SizeBytes    int64             `json:"sizeBytes"`
	LastModified string            `json:"lastModified"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// ID is the bundle ID Lumen stamped on the object, or "" if none.
func (b Bundle) ID() string { return b.Metadata[BundleIDMetadata] }

// ListResult is the decoded stdout of bundle-manager list.
type ListResult struct {
	Bundles []Bundle
	Raw     []byte
}

type listReport struct {
	Version string   `json:"version"`
	Bundles []Bundle `json:"bundles"`
	Failure *struct {
		Stage   string `json:"stage"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"failure,omitempty"`
}

// ParseList decodes bundle-manager list output.
func ParseList(raw []byte) (*ListResult, error) {
	var report listReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode bundle-manager list output: %w", err)
	}
	if report.Version != "v1" {
		return nil, fmt.Errorf("bundle-manager list returned unsupported version %q", report.Version)
	}
	if report.Failure != nil {
		return nil, fmt.Errorf("bundle-manager list failed (%s/%s): %s", report.Failure.Stage, report.Failure.Reason, report.Failure.Message)
	}
	return &ListResult{Bundles: report.Bundles, Raw: raw}, nil
}

// List runs bundle-manager list, optionally filtered by exact metadata matches.
func (s *Session) List(ctx context.Context, metadata map[string]string) (*ListResult, error) {
	args := []string{"list"}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "--metadata", k+"="+metadata[k])
	}
	var stdout bytes.Buffer
	if err := s.BundleManager(ctx, &stdout, nil, args...); err != nil {
		return nil, err
	}
	return ParseList(stdout.Bytes())
}

// StoredStatus is the status of a stored bundle whose collection Job is gone.
func StoredStatus(id string, bundle Bundle) *CollectionStatus {
	return &CollectionStatus{ID: id, Phase: PhaseUnknown, Message: "the collection Job is gone", Stored: &bundle}
}

// ErrBundleNotFound means no stored bundle matched an ID or location.
var ErrBundleNotFound = errors.New("support bundle not found")

// IsLocation reports whether ref is an object URI rather than a bundle ID.
func IsLocation(ref string) bool { return strings.HasPrefix(ref, "s3://") }

// Resolve finds the one stored bundle for an ID or an exact s3:// location.
func (s *Session) Resolve(ctx context.Context, ref string) (Bundle, error) {
	if IsLocation(ref) {
		result, err := s.List(ctx, nil)
		if err != nil {
			return Bundle{}, err
		}
		for _, b := range result.Bundles {
			if b.Location == ref {
				return b, nil
			}
		}
		return Bundle{}, fmt.Errorf("%w: no support bundle stored at %s", ErrBundleNotFound, ref)
	}
	if !bundleIDPattern.MatchString(ref) {
		return Bundle{}, fmt.Errorf("%q is neither a bundle ID nor an s3:// location", ref)
	}
	result, err := s.List(ctx, map[string]string{BundleIDMetadata: ref})
	if err != nil {
		return Bundle{}, err
	}
	switch len(result.Bundles) {
	case 1:
		return result.Bundles[0], nil
	case 0:
		return Bundle{}, fmt.Errorf("%w: no stored bundle has ID %s", ErrBundleNotFound, ref)
	default:
		locations := make([]string, len(result.Bundles))
		for i, b := range result.Bundles {
			locations[i] = "  " + b.Location
		}
		return Bundle{}, fmt.Errorf("%d bundles have ID %s - pass one location instead:\n%s", len(result.Bundles), ref, strings.Join(locations, "\n"))
	}
}

// OpenSession checks the template and starts a manager Job for op. The caller
// must Close the session.
func OpenSession(ctx context.Context, client kubernetes.Interface, executor Executor, cr *v2.WeightsAndBiases, op Operation, progress func(string)) (*Session, error) {
	template, err := Template(ctx, client, cr)
	if err != nil {
		return nil, err
	}
	return StartSession(ctx, client, executor, cr, template, op, SessionOptions{Lifetime: managerLifetime, Progress: progress})
}

// Entry is one row of `wsm support-bundle list`: a stored bundle, or a
// collection that hasn't uploaded yet.
type Entry struct {
	ID        string    `json:"id,omitempty"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created"`
	SizeBytes int64     `json:"sizeBytes,omitempty"`
	Location  string    `json:"location,omitempty"`
}

const (
	EntryCollecting = "collecting"
	EntryUploaded   = "uploaded"
)

// Entries merges stored bundles with running collections. A running ID that
// already has a stored bundle is shown once, as uploaded.
func Entries(bundles []Bundle, running []batchv1.Job) []Entry {
	entries := make([]Entry, 0, len(bundles)+len(running))
	stored := map[string]bool{}
	for _, b := range bundles {
		created, _ := time.Parse(time.RFC3339, b.LastModified)
		entries = append(entries, Entry{ID: b.ID(), Status: EntryUploaded, Created: created, SizeBytes: b.SizeBytes, Location: b.Location})
		stored[b.ID()] = true
	}
	for _, job := range running {
		if !stored[job.Name] {
			entries = append(entries, Entry{ID: job.Name, Status: EntryCollecting, Created: job.CreationTimestamp.Time})
		}
	}
	return entries
}

// SortKeys and SortOrders are the values SortEntries accepts.
var (
	SortKeys   = []string{"created", "size", "id", "status"}
	SortOrders = []string{"ascending", "descending", "asc", "desc"}
)

// SortEntries sorts by key. An empty order uses the key's natural one:
// newest and largest first, IDs and statuses A-Z.
func SortEntries(entries []Entry, by, order string) error {
	var compare func(a, b Entry) int
	descending := false
	switch by {
	case "created":
		compare, descending = func(a, b Entry) int { return a.Created.Compare(b.Created) }, true
	case "size":
		compare, descending = func(a, b Entry) int { return cmp.Compare(a.SizeBytes, b.SizeBytes) }, true
	case "id":
		compare = func(a, b Entry) int { return strings.Compare(a.ID, b.ID) }
	case "status":
		compare = func(a, b Entry) int { return strings.Compare(a.Status, b.Status) }
	default:
		return fmt.Errorf("unknown sort key %q - use one of %s", by, strings.Join(SortKeys, ", "))
	}
	switch order {
	case "":
	case "asc", "ascending":
		descending = false
	case "desc", "descending":
		descending = true
	default:
		return fmt.Errorf("unknown sort order %q - use one of %s", order, strings.Join(SortOrders, ", "))
	}
	if descending {
		ascending := compare
		compare = func(a, b Entry) int { return ascending(b, a) }
	}
	if by == "status" {
		primary := compare
		compare = func(a, b Entry) int { return cmp.Or(primary(a, b), b.Created.Compare(a.Created)) }
	}
	slices.SortStableFunc(entries, compare)
	return nil
}
