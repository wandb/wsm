package supportbundle

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ResultPathEnv is the env var the collector honours for the v1 result
// contract (shared with the operator's SupportBundleRequest controller).
const ResultPathEnv = "LUMEN_SUPPORT_BUNDLE_RESULT_PATH"

// Result is Lumen's v1 termination-message result. SizeBytes and SHA256 are
// optional wsm extensions; the operator's reader ignores unknown fields.
type Result struct {
	Version  string    `json:"version"`
	Artifact *Artifact `json:"artifact,omitempty"`
	Failure  *Failure  `json:"failure,omitempty"`
}

type Artifact struct {
	Location  string `json:"location"`
	Partial   bool   `json:"partial,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type Failure struct {
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

var (
	reasonPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_,:]*$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	validStages   = map[string]bool{
		"Validation": true, "Startup": true, "Collection": true, "Upload": true,
		"Timeout": true, "ResultReporting": true, "Workload": true,
	}
)

const maxFailureMessage = 2048

// ParseResult decodes and validates a termination message. An empty message
// returns (nil, nil): the collector did not report a result.
func ParseResult(message string) (*Result, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, nil
	}
	var r Result
	if err := json.Unmarshal([]byte(message), &r); err != nil {
		return nil, fmt.Errorf("result is not valid JSON")
	}
	if r.Version != "v1" {
		return nil, fmt.Errorf("unsupported result version %q", r.Version)
	}
	if (r.Artifact == nil) == (r.Failure == nil) {
		return nil, fmt.Errorf("result must contain exactly one of artifact or failure")
	}
	if r.Artifact != nil {
		if err := validateLocation(r.Artifact.Location); err != nil {
			return nil, err
		}
		if r.Artifact.SizeBytes < 0 {
			return nil, fmt.Errorf("result artifact size is negative")
		}
		if r.Artifact.SHA256 != "" && !sha256Pattern.MatchString(r.Artifact.SHA256) {
			return nil, fmt.Errorf("result artifact sha256 is malformed")
		}
	}
	if r.Failure != nil {
		if !validStages[r.Failure.Stage] {
			return nil, fmt.Errorf("result failure stage %q is not recognised", r.Failure.Stage)
		}
		if !reasonPattern.MatchString(r.Failure.Reason) {
			return nil, fmt.Errorf("result failure reason is malformed")
		}
		r.Failure.Message = sanitizeMessage(r.Failure.Message)
	}
	return &r, nil
}

// validateLocation accepts only credential-free bucket URIs.
func validateLocation(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("result artifact location is not a URI")
	}
	switch u.Scheme {
	case "s3", "gs", "az":
	default:
		return fmt.Errorf("result artifact location scheme %q is not supported", u.Scheme)
	}
	if u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("result artifact location must name a bucket and object")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return fmt.Errorf("result artifact location must not contain credentials, query parameters, or fragments")
	}
	return nil
}

// sanitizeMessage drops anything that could carry a URL (and with it
// credentials or signed links) and caps the length.
func sanitizeMessage(msg string) string {
	if strings.Contains(msg, "://") {
		return "collector reported an error (details withheld because they contained a URL)"
	}
	if len(msg) > maxFailureMessage {
		msg = msg[:maxFailureMessage]
	}
	return msg
}
