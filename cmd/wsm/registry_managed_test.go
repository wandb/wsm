package main

import (
	"sort"
	"strings"
	"testing"
)

func TestParseManagedExclusions_KafkaRejected(t *testing.T) {
	if _, err := parseManagedExclusions([]string{"kafka"}, nil, false); err == nil {
		t.Error("--exclude-operators kafka should be rejected")
	} else if !strings.Contains(err.Error(), "kafka cannot be excluded") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := parseManagedExclusions(nil, []string{"kafka"}, false); err == nil {
		t.Error("--exclude-managed kafka should be rejected")
	}
}

func TestParseManagedExclusions_Unknown(t *testing.T) {
	if _, err := parseManagedExclusions(nil, []string{"postgres"}, false); err == nil {
		t.Error("unknown managed type should be rejected")
	}
}

func TestManagedExclusions_Semantics(t *testing.T) {
	// --exclude-operators mysql: operator skipped, server kept.
	exclusions, err := parseManagedExclusions([]string{"mysql"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !exclusions.excludeOperator("mysql") {
		t.Error("mysql operator should be excluded")
	}
	if exclusions.excludeServer("mysql") {
		t.Error("mysql server should NOT be excluded by --exclude-operators")
	}

	// --exclude-managed redis: both operator and server skipped.
	exclusions, err = parseManagedExclusions(nil, []string{"redis"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !exclusions.excludeOperator("redis") || !exclusions.excludeServer("redis") {
		t.Error("--exclude-managed redis should exclude both operator and server")
	}
}

func TestManagedExclusions_SkipAllAliasAndSubcharts(t *testing.T) {
	exclusions, err := parseManagedExclusions(nil, nil, true) // --skip-managed-images
	if err != nil {
		t.Fatal(err)
	}
	if !exclusions.allOperatorsExcluded() {
		t.Error("--skip-managed-images should exclude all operators")
	}
	for _, tp := range []string{"clickhouse", "mysql", "redis", "object-store"} {
		if !exclusions.excludeServer(tp) {
			t.Errorf("--skip-managed-images should exclude server %q", tp)
		}
	}

	// disabledSubcharts maps the excluded operator types to their subchart names.
	exclusions, err = parseManagedExclusions([]string{"mysql", "clickhouse"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	got := exclusions.disabledSubcharts()
	sort.Strings(got)
	want := []string{"altinity-clickhouse-operator", "moco"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("disabledSubcharts = %v, want %v", got, want)
	}
	if exclusions.allOperatorsExcluded() {
		t.Error("only two of four types excluded — allOperatorsExcluded should be false")
	}
}
