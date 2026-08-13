package main

import (
	"fmt"
	"sort"
	"strings"
)

// managedType is an excludable managed-service type. Kafka is deliberately not one:
// its data-plane images are always mirrored and it has no operator subchart.
type managedType struct {
	// subchart is the operator-chart dependency for this type's operator, disabled
	// via <subchart>.enabled=false to drop its images from the render.
	subchart string
}

// managedTypes is the set of excludable managed-service types, keyed by the value
// accepted on the --exclude-operators / --exclude-managed flags.
var managedTypes = map[string]managedType{
	"clickhouse":   {subchart: "altinity-clickhouse-operator"},
	"mysql":        {subchart: "moco"},
	"redis":        {subchart: "redis-operator"},
	"object-store": {subchart: "seaweedfs-operator"},
}

func managedTypeNames() []string {
	names := make([]string, 0, len(managedTypes))
	for n := range managedTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// managedExclusions records excluded managed-service types along two axes:
// operators (skip the operator images only — customer runs their own operator) and
// managed (skip the type entirely — customer brings an external service). Excluding
// the managed service implies excluding its operator.
type managedExclusions struct {
	operators map[string]bool
	managed   map[string]bool
}

// excludeOperator reports whether the type's operator images should be skipped
// (either axis excludes the operator).
func (e managedExclusions) excludeOperator(t string) bool {
	return e.operators[t] || e.managed[t]
}

func (e managedExclusions) excludeServer(t string) bool {
	return e.managed[t]
}

// disabledSubcharts returns the subcharts to disable when rendering the operator
// chart, one per type whose operator is excluded.
func (e managedExclusions) disabledSubcharts() []string {
	var out []string
	for _, t := range managedTypeNames() {
		if e.excludeOperator(t) {
			out = append(out, managedTypes[t].subchart)
		}
	}
	return out
}

// allOperatorsExcluded reports whether the operator chart can be skipped entirely.
func (e managedExclusions) allOperatorsExcluded() bool {
	for t := range managedTypes {
		if !e.excludeOperator(t) {
			return false
		}
	}
	return true
}

// parseManagedExclusions validates the --exclude-operators / --exclude-managed
// values (and the --skip-managed-images alias) into a managedExclusions. Unknown
// types — including "kafka", which can never be excluded — are rejected.
func parseManagedExclusions(excludeOperators, excludeManaged []string, skipManaged bool) (managedExclusions, error) {
	exclusions := managedExclusions{operators: map[string]bool{}, managed: map[string]bool{}}

	assign := func(flag string, values []string, dst map[string]bool) error {
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if _, ok := managedTypes[v]; !ok {
				if v == "kafka" {
					return fmt.Errorf("%s: kafka cannot be excluded — its images are always mirrored", flag)
				}
				return fmt.Errorf("%s: unknown managed type %q (valid: %s)", flag, v, strings.Join(managedTypeNames(), ", "))
			}
			dst[v] = true
		}
		return nil
	}

	if err := assign("--exclude-operators", excludeOperators, exclusions.operators); err != nil {
		return exclusions, err
	}
	if err := assign("--exclude-managed", excludeManaged, exclusions.managed); err != nil {
		return exclusions, err
	}
	if skipManaged {
		for t := range managedTypes {
			exclusions.managed[t] = true
		}
	}
	return exclusions, nil
}
