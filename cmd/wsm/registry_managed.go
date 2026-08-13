package main

import (
	"fmt"
	"sort"
	"strings"
)

// managedType describes a managed-service type that can be excluded from the
// mirror. Kafka is deliberately NOT a managed type: its data-plane images are
// always mirrored and it has no operator subchart.
type managedType struct {
	// subchart is the operator-chart dependency that installs this type's operator;
	// disabling it (via <subchart>.enabled=false) drops the operator's images from
	// the render.
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

// managedExclusions records which managed-service types are excluded from the
// mirror, along two independent axes:
//
//   - operators: skip the type's OPERATOR images only — the customer runs their own
//     cluster-wide operator, but still uses W&B's managed data-plane service.
//   - managed: skip the type ENTIRELY (operator + data-plane server images) — the
//     customer brings an external service.
//
// Excluding a type's managed service implies excluding its now-purposeless
// operator, so managed is a superset for the operator axis.
type managedExclusions struct {
	operators map[string]bool
	managed   map[string]bool
}

// excludeOperator reports whether the type's operator images should be skipped.
func (e managedExclusions) excludeOperator(t string) bool {
	return e.operators[t] || e.managed[t]
}

// excludeServer reports whether the type's data-plane server images should be skipped.
func (e managedExclusions) excludeServer(t string) bool {
	return e.managed[t]
}

// disabledSubcharts returns the operator-chart subcharts to disable when rendering
// the operator chart — one per type whose operator is excluded — so their operator
// images (and, for moco, the injected sidecars) are not derived.
func (e managedExclusions) disabledSubcharts() []string {
	var out []string
	for _, t := range managedTypeNames() {
		if e.excludeOperator(t) {
			out = append(out, managedTypes[t].subchart)
		}
	}
	return out
}

// allOperatorsExcluded reports whether every managed operator is excluded, so the
// operator chart need not be rendered for managed images at all.
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
	ex := managedExclusions{operators: map[string]bool{}, managed: map[string]bool{}}

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

	if err := assign("--exclude-operators", excludeOperators, ex.operators); err != nil {
		return ex, err
	}
	if err := assign("--exclude-managed", excludeManaged, ex.managed); err != nil {
		return ex, err
	}
	if skipManaged {
		for t := range managedTypes {
			ex.managed[t] = true
		}
	}
	return ex, nil
}
