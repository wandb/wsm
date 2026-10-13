package telemetry

import (
	"context"
	"fmt"
	"time"
)

// LatencyWindow is the rate window for the latency quantiles. A rolling window
// gives a meaningful "current" latency with no stored history on our side —
// VictoriaMetrics computes it per request from the scraped histogram.
const LatencyWindow = "5m"

// Quantiles are request-latency percentiles in seconds. A nil field means the
// query returned no data (metric absent, or no traffic in the window), which the
// UI renders as "no data" rather than a zero.
type Quantiles struct {
	P50 *float64 `json:"p50,omitempty"`
	P90 *float64 `json:"p90,omitempty"`
	P99 *float64 `json:"p99,omitempty"`
}

// LatencySnapshot is a point-in-time view of request latency for an instance.
type LatencySnapshot struct {
	WindowSeconds int       `json:"windowSeconds"`
	Overall       Quantiles `json:"overall"`
}

// latencyMetric is the W&B app's request-latency histogram. The app is
// instrumented with OpenTelemetry tracing, and the operator's telemetry stack
// derives RED metrics from those spans via the spanmetrics connector — so latency
// lives here, in milliseconds, not in a classic http_request_duration_seconds.
// Verified against a live operator install (services gorilla, gorilla-*, anaconda2).
const latencyMetric = "traces_spanmetrics_duration_milliseconds_bucket"

// spanKindServer restricts to inbound request spans. CLIENT/INTERNAL spans are the
// app's own outbound calls (redis, sql, …), not the request latency we report.
const spanKindServer = `span_kind="SPAN_KIND_SERVER"`

// latencyQuantileQuery is the PromQL for one request-latency percentile over
// LatencyWindow, aggregated across the app's server spans. Span metrics are in
// milliseconds; dividing by 1000 yields seconds to match LatencySnapshot.
func latencyQuantileQuery(q float64) string {
	return fmt.Sprintf(
		`histogram_quantile(%g, sum(rate(%s{%s}[%s])) by (le)) / 1000`,
		q, latencyMetric, spanKindServer, LatencyWindow,
	)
}

// FetchLatency runs the p50/p90/p99 latency queries and returns a snapshot. A
// quantile that returns no series is left nil rather than failing the whole call;
// only a transport or query error aborts.
func FetchLatency(ctx context.Context, c *QueryClient) (LatencySnapshot, error) {
	window, _ := time.ParseDuration(LatencyWindow)
	snap := LatencySnapshot{WindowSeconds: int(window.Seconds())}

	for _, q := range []struct {
		p   float64
		dst **float64
	}{
		{0.50, &snap.Overall.P50},
		{0.90, &snap.Overall.P90},
		{0.99, &snap.Overall.P99},
	} {
		v, ok, err := c.InstantScalar(ctx, latencyQuantileQuery(q.p))
		if err != nil {
			return LatencySnapshot{}, err
		}
		if ok {
			val := v
			*q.dst = &val
		}
	}
	return snap, nil
}
