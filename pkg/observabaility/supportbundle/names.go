// Package supportbundle runs Lumen support-bundle collection for a v2 W&B
// installation as a wsm-created Kubernetes Job and tracks each run with a
// per-bundle record ConfigMap.
package supportbundle

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// DefaultLumenVersion is the Lumen image tag wsm runs. Lumen has not cut a
	// release with support-bundle mode yet, so this pins a main-branch commit
	// (CI publishes immutable :<sha> tags). Bump by hand.
	DefaultLumenVersion = "f346a1233b9ca8fe172eee563e74521c01ae7e66"
	// LumenRepository is appended to the image registry.
	LumenRepository = "wandb/lumen"
	// VictoriaHelperImage is the helper pod image Lumen's victoriaStack
	// collector hardcodes; listed here so `wsm registry mirror` ships it.
	VictoriaHelperImage = "curlimages/curl:8.21.0"

	// DefaultServiceAccountName is the stable identity the collector runs as.
	DefaultServiceAccountName = "wsm-lumen"

	LabelManagedBy    = "app.kubernetes.io/managed-by"
	LabelComponent    = "app.kubernetes.io/component"
	LabelInstallation = "support-bundle.wsm.wandb.ai/installation"
	LabelBundleID     = "support-bundle.wsm.wandb.ai/bundle-id"
	LabelRole         = "support-bundle.wsm.wandb.ai/role"

	managedByWSM = "wsm"
	component    = "support-bundle"

	roleRecord = "record"
	roleSpec   = "spec"
	roleJob    = "job"
	roleSetup  = "setup"

	containerName = "lumen"
	fieldManager  = "wsm"
)

// identityAnnotations are the workload-identity annotations copied from the
// app ServiceAccount onto the collector ServiceAccount.
var identityAnnotations = []string{
	"eks.amazonaws.com/role-arn",
	"iam.gke.io/gcp-service-account",
	"azure.workload.identity/client-id",
}

// fitName returns base if it fits in max chars, otherwise a truncated prefix
// plus a short hash so distinct inputs stay distinct.
func fitName(base string, max int) string {
	if len(base) <= max {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	suffix := "-" + hex.EncodeToString(sum[:])[:5]
	return strings.TrimRight(base[:max-len(suffix)], "-.") + suffix
}

// installationKey is the label value identifying an installation.
func installationKey(namespace, name string) string {
	return fitName(namespace+"."+name, 63)
}

// clusterScopedName names per-installation cluster-scoped RBAC objects.
func clusterScopedName(namespace, name string) string {
	return fitName("wsm-lumen-"+namespace+"-"+name, 63)
}

// NewBundleID returns a random bundle ID of the form sb-<6 hex>.
func NewBundleID() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate bundle id: %w", err)
	}
	return "sb-" + hex.EncodeToString(b), nil
}

// ValidBundleID reports whether id has the sb-<6 hex> shape.
func ValidBundleID(id string) bool {
	if len(id) != 9 || !strings.HasPrefix(id, "sb-") {
		return false
	}
	_, err := hex.DecodeString(id[3:])
	return err == nil
}

func recordName(id string) string { return "wsm-sb-" + id }
func jobName(id string) string    { return "support-bundle-" + strings.TrimPrefix(id, "sb-") }
func specName(id string) string   { return jobName(id) + "-spec" }

func baseLabels(namespace, name string) map[string]string {
	return map[string]string{
		LabelManagedBy:    managedByWSM,
		LabelComponent:    component,
		LabelInstallation: installationKey(namespace, name),
	}
}

func bundleLabels(namespace, name, id, role string) map[string]string {
	l := baseLabels(namespace, name)
	l[LabelBundleID] = id
	l[LabelRole] = role
	return l
}

func setupLabels(namespace, name string) map[string]string {
	l := baseLabels(namespace, name)
	l[LabelRole] = roleSetup
	return l
}

// ownedBy reports whether labels mark an object as wsm-managed for installation key.
func ownedBy(labels map[string]string, key string) bool {
	return labels[LabelManagedBy] == managedByWSM &&
		labels[LabelComponent] == component &&
		labels[LabelInstallation] == key
}

func selectorFor(namespace, name, role string) string {
	return fmt.Sprintf("%s=%s,%s=%s,%s=%s,%s=%s",
		LabelManagedBy, managedByWSM,
		LabelComponent, component,
		LabelInstallation, installationKey(namespace, name),
		LabelRole, role)
}
