package supportbundle

import (
	"fmt"
	"net/url"
	"strings"

	v2 "github.com/wandb/operator/api/v2"
	corev1 "k8s.io/api/core/v1"
)

// Installation is the subset of a v2 WeightsAndBiases CR that support-bundle
// collection needs. Credentials are carried only as Secret references.
type Installation struct {
	Namespace string
	Name      string

	Ready      bool
	Conditions []string

	// ObjectStore is status.objectStoreStatus["default"].connection.
	ObjectStore      v2.ObjectStoreConnection
	ObjectStoreReady bool

	Telemetry *Telemetry

	ImageRegistry       string
	ImagePullSecrets    []corev1.LocalObjectReference
	AppServiceAccount   string
	IdentityAnnotations map[string]string
	Affinity            *corev1.Affinity
	Tolerations         []corev1.Toleration
}

// Telemetry holds the Victoria read endpoints reported by the operator.
type Telemetry struct {
	Mode            string
	Namespace       string
	Ready           bool
	MetricsEndpoint string
	LogsEndpoint    string
	TracesEndpoint  string
}

// FromCR extracts and validates what collection needs from the CR. Warnings
// describe optional sources that will be skipped.
func FromCR(cr *v2.WeightsAndBiases) (*Installation, []string, error) {
	inst := &Installation{
		Namespace:        cr.Namespace,
		Name:             cr.Name,
		Ready:            cr.Status.Ready,
		ImageRegistry:    strings.TrimRight(cr.Spec.Global.ImageRegistry, "/"),
		ImagePullSecrets: cr.Spec.Global.ImagePullSecrets,
		Affinity:         cr.Spec.Affinity,
	}
	if cr.Spec.Tolerations != nil {
		inst.Tolerations = *cr.Spec.Tolerations
	}
	for _, c := range cr.Status.Conditions {
		inst.Conditions = append(inst.Conditions, fmt.Sprintf("%s=%s (%s)", c.Type, c.Status, c.Reason))
	}

	inst.AppServiceAccount = cr.Spec.Wandb.ServiceAccount.ServiceAccountName
	if inst.AppServiceAccount == "" {
		inst.AppServiceAccount = "wandb"
	}
	for _, key := range identityAnnotations {
		if v, ok := cr.Spec.Wandb.ServiceAccount.Annotations[key]; ok {
			if inst.IdentityAnnotations == nil {
				inst.IdentityAnnotations = map[string]string{}
			}
			inst.IdentityAnnotations[key] = v
		}
	}

	objectStore, ok := v2.ResolveInstance(cr.Status.ObjectStoreStatus, v2.DefaultInstanceName)
	if !ok {
		return nil, nil, fmt.Errorf("WeightsAndBiases %s/%s reports no object store connection in status; wait for the operator to reconcile it", cr.Namespace, cr.Name)
	}
	inst.ObjectStore = objectStore.Connection
	inst.ObjectStoreReady = objectStore.Ready
	if err := validateObjectStore(&inst.ObjectStore); err != nil {
		return nil, nil, fmt.Errorf("WeightsAndBiases %s/%s: %w", cr.Namespace, cr.Name, err)
	}

	var warnings []string
	inst.Telemetry, warnings = telemetryFrom(cr, inst.Namespace)
	return inst, warnings, nil
}

// validateObjectStore requires the upload URL and credentials to be Secret
// references so no credential ever lands in a pod spec or ConfigMap.
func validateObjectStore(c *v2.ObjectStoreConnection) error {
	if c.URL.SecretKeyRef() == nil {
		return fmt.Errorf("object store connection url is not a Secret reference")
	}
	for name, v := range map[string]*v2.ValueOrSecret{"accessKey": &c.AccessKey, "secretKey": &c.SecretKey} {
		if v.Value != "" {
			return fmt.Errorf("object store connection %s is a literal value; it must be a Secret reference", name)
		}
	}
	return nil
}

func telemetryFrom(cr *v2.WeightsAndBiases, fallbackNamespace string) (*Telemetry, []string) {
	status := cr.Status.TelemetryStatus
	if status.Mode != "forward" && status.Mode != "full" {
		return nil, nil
	}
	t := &Telemetry{
		Mode:            status.Mode,
		Namespace:       status.Connection.ManagedNamespace,
		Ready:           status.Ready,
		MetricsEndpoint: status.Connection.MetricsReadEndpoint,
		LogsEndpoint:    status.Connection.LogsReadEndpoint,
		TracesEndpoint:  status.Connection.TracesReadEndpoint,
	}
	if t.Namespace == "" {
		t.Namespace = fallbackNamespace
	}

	var warnings []string
	for _, ep := range []*string{&t.MetricsEndpoint, &t.LogsEndpoint, &t.TracesEndpoint} {
		if *ep != "" && !validReadEndpoint(*ep) {
			warnings = append(warnings, fmt.Sprintf("ignoring invalid telemetry read endpoint %q", *ep))
			*ep = ""
		}
	}
	if t.MetricsEndpoint == "" && t.LogsEndpoint == "" && t.TracesEndpoint == "" {
		return nil, append(warnings, "telemetry is enabled but reports no read endpoints; skipping Victoria collection")
	}
	if !t.Ready {
		return nil, append(warnings, "telemetry stack is not ready; skipping Victoria collection")
	}
	return t, warnings
}

func validReadEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// InstallationRef identifies a WeightsAndBiases resource.
type InstallationRef struct {
	Namespace string
	Name      string
}

func (r InstallationRef) String() string { return r.Namespace + "/" + r.Name }

// SelectInstallation picks the single installation matching the optional
// namespace/name filters, so users only pass flags when the cluster is ambiguous.
func SelectInstallation(refs []InstallationRef, namespace, name string) (InstallationRef, error) {
	var matches []InstallationRef
	for _, r := range refs {
		if (namespace == "" || r.Namespace == namespace) && (name == "" || r.Name == name) {
			matches = append(matches, r)
		}
	}
	scope := "in the cluster"
	if namespace != "" {
		scope = "in namespace " + namespace
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if name != "" {
			return InstallationRef{}, fmt.Errorf("no v2 WeightsAndBiases named %q found %s", name, scope)
		}
		return InstallationRef{}, fmt.Errorf("no v2 WeightsAndBiases found %s", scope)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.String()
		}
		return InstallationRef{}, fmt.Errorf("found %d WeightsAndBiases %s (%s); select one with --wandb-namespace and --wandb-name",
			len(matches), scope, strings.Join(names, ", "))
	}
}

// ResolveImage returns the Lumen image reference, refusing floating tags.
func ResolveImage(inst *Installation, registryOverride, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" || version == "latest" {
		return "", fmt.Errorf("a pinned Lumen version is required (got %q)", version)
	}
	registry := strings.TrimRight(registryOverride, "/")
	if registry == "" {
		registry = inst.ImageRegistry
	}
	if registry == "" {
		registry = v2.DefaultImageRegistry
	}
	return registry + "/" + LumenRepository + ":" + version, nil
}
