package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// QueryClient runs instant PromQL queries against a Prometheus-compatible HTTP
// API — VictoriaMetrics' vmsingle (see the "victoria" Catalog entry, port 8428).
// It holds no cluster state: the caller resolves the service address (InClusterURL)
// and passes it in, so the same client works in-cluster or behind a port-forward.
type QueryClient struct {
	baseURL string
	http    *http.Client
}

// NewQueryClient builds a client against a VictoriaMetrics base URL (no trailing
// /api/v1 — that's appended per query).
func NewQueryClient(baseURL string) *QueryClient {
	return &QueryClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// InClusterURL is the base URL of a telemetry UI's Service as reached from inside
// the cluster — watchtower-in-cluster talks to the ClusterIP directly, so this is
// the cluster-mode address for the QueryClient.
func InClusterURL(ui UI, namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", ui.Service, namespace, ui.Port)
}

// promResponse is the Prometheus/VictoriaMetrics instant-query envelope.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

// InstantScalar runs an instant query and returns the value of the first series.
// ok is false when the query succeeds but returns no series (the metric does not
// exist yet, or there is no traffic in the window) or the value is NaN/Inf —
// callers treat that as "no data" rather than an error. Only a transport or query
// failure returns a non-nil error.
func (c *QueryClient) InstantScalar(ctx context.Context, promql string) (value float64, ok bool, err error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", c.baseURL, url.QueryEscape(promql))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("query victoriametrics: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("victoriametrics returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, false, fmt.Errorf("decode query response: %w", err)
	}
	if pr.Status != "success" {
		return 0, false, fmt.Errorf("query failed: %s: %s", pr.ErrorType, pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, false, nil
	}

	// The sample is [ <unix ts>, "<value>" ]; the value is the JSON string element.
	var raw string
	if err := json.Unmarshal(pr.Data.Result[0].Value[1], &raw); err != nil {
		return 0, false, fmt.Errorf("decode sample value: %w", err)
	}
	// PromQL renders NaN/+Inf/-Inf as those literal strings; treat them as no data.
	f, perr := strconv.ParseFloat(raw, 64)
	if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false, nil
	}
	return f, true, nil
}
