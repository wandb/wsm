package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstantScalar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("query") {
		case "value":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"0.1234"]}]}}`))
		case "empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		case "nan":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"NaN"]}]}}`))
		case "boom":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"internal","error":"kaboom"}`))
		}
	}))
	defer srv.Close()

	c := NewQueryClient(srv.URL)
	ctx := context.Background()

	if v, ok, err := c.InstantScalar(ctx, "value"); err != nil || !ok || v != 0.1234 {
		t.Fatalf("value: got (%v, %v, %v), want (0.1234, true, nil)", v, ok, err)
	}
	if _, ok, err := c.InstantScalar(ctx, "empty"); err != nil || ok {
		t.Fatalf("empty: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if _, ok, err := c.InstantScalar(ctx, "nan"); err != nil || ok {
		t.Fatalf("nan: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if _, _, err := c.InstantScalar(ctx, "boom"); err == nil {
		t.Fatalf("boom: expected an error on a 500 response")
	}
}

func TestFetchLatency(t *testing.T) {
	// Return a distinct value per quantile, and no data for p99 to exercise the
	// nil-field path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case contains(q, "0.5"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"0.05"]}]}}`))
		case contains(q, "0.9") && !contains(q, "0.99"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"0.2"]}]}}`))
		default: // p99 → no series
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		}
	}))
	defer srv.Close()

	snap, err := FetchLatency(context.Background(), NewQueryClient(srv.URL))
	if err != nil {
		t.Fatalf("FetchLatency: %v", err)
	}
	if snap.WindowSeconds != 300 {
		t.Errorf("windowSeconds = %d, want 300", snap.WindowSeconds)
	}
	if snap.Overall.P50 == nil || *snap.Overall.P50 != 0.05 {
		t.Errorf("p50 = %v, want 0.05", snap.Overall.P50)
	}
	if snap.Overall.P90 == nil || *snap.Overall.P90 != 0.2 {
		t.Errorf("p90 = %v, want 0.2", snap.Overall.P90)
	}
	if snap.Overall.P99 != nil {
		t.Errorf("p99 = %v, want nil (no data)", *snap.Overall.P99)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
