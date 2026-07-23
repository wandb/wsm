package kubectl

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
)

// CreateDeploymentMarker creates a ConfigMap marker to track wsm-managed deployments
// Note: Assumes the namespace already exists (created by operator manifest)
func CreateDeploymentMarker(ctx context.Context, clusterName, namespace string, components string) error {
	data := map[string]string{
		"created-by": "wsm",
		"components": components,
	}

	if clusterName != "" {
		data["cluster-name"] = clusterName
	}

	if err := UpsertConfigMap(data, "wsm-deployment-marker", namespace); err != nil {
		return fmt.Errorf("failed to create deployment marker: %w", err)
	}

	return nil
}

// HasDeploymentMarker checks if a deployment marker exists
func HasDeploymentMarker(ctx context.Context, namespace string, component string) (bool, error) {
	configMap, err := GetConfigMap(ctx, "wsm-deployment-marker", namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check for deployment marker: %w", err)
	}

	if components, ok := configMap.Data["components"]; ok {
		for c := range strings.SplitSeq(components, ",") {
			if strings.TrimSpace(c) == component {
				return true, nil
			}
		}
	}

	return false, nil
}

// DeleteDeploymentMarker removes the specified component from the deployment marker ConfigMap
// if component is empty, it removes the entire deployment marker.
// if after removing the component, no components remain, it deletes the marker.
func DeleteDeploymentMarker(ctx context.Context, namespace string, component string) error {
	if component == "" {
		if err := DeleteConfigMap(ctx, "wsm-deployment-marker", namespace); err != nil {
			return fmt.Errorf("failed to delete deployment marker: %w", err)
		}
		return nil
	}

	cm, err := GetConfigMap(ctx, "wsm-deployment-marker", namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to check for deployment marker: %w", err)
	}

	componentsStr, ok := cm.Data["components"]
	if !ok {
		return DeleteConfigMap(ctx, "wsm-deployment-marker", namespace)
	}

	var newComponents []string
	for c := range strings.SplitSeq(componentsStr, ",") {
		c = strings.TrimSpace(c)
		if c != "" && c != component {
			newComponents = append(newComponents, c)
		}
	}

	if len(newComponents) == 0 {
		if err := DeleteConfigMap(ctx, "wsm-deployment-marker", namespace); err != nil {
			return fmt.Errorf("failed to delete deployment marker: %w", err)
		}
		return nil
	}

	cm.Data["components"] = strings.Join(newComponents, ",")
	if err := UpsertConfigMap(cm.Data, "wsm-deployment-marker", namespace); err != nil {
		return fmt.Errorf("failed to update deployment marker: %w", err)
	}

	return nil
}

// LoadDisabledSubcharts returns the operator subcharts previously disabled via the
// --enable-* flags, read from the "disabled-subcharts" key of the deployment marker
// ConfigMap. Returns an empty slice when the marker or the key is absent, so a
// first install (no marker yet) sees nothing disabled.
func LoadDisabledSubcharts(ctx context.Context, namespace string) ([]string, error) {
	cm, err := GetConfigMap(ctx, "wsm-deployment-marker", namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read deployment marker: %w", err)
	}

	raw, ok := cm.Data["disabled-subcharts"]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var disabled []string
	for c := range strings.SplitSeq(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			disabled = append(disabled, c)
		}
	}
	return disabled, nil
}

// SaveDisabledSubcharts persists the disabled subchart list into the "disabled-subcharts"
// key of the deployment marker ConfigMap, preserving the marker's other data. An empty
// list removes the key. The marker ConfigMap is expected to already exist (created by
// CreateDeploymentMarker); if it does not and there is nothing to persist, this is a no-op
// (e.g. clearing state during destroy when the marker is already gone).
func SaveDisabledSubcharts(ctx context.Context, namespace string, disabled []string) error {
	cm, err := GetConfigMap(ctx, "wsm-deployment-marker", namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			if len(disabled) == 0 {
				return nil
			}
			return fmt.Errorf("cannot save disabled subcharts: deployment marker not found in namespace %q", namespace)
		}
		return fmt.Errorf("failed to read deployment marker: %w", err)
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if len(disabled) == 0 {
		delete(cm.Data, "disabled-subcharts")
	} else {
		cm.Data["disabled-subcharts"] = strings.Join(disabled, ",")
	}

	if err := UpsertConfigMap(cm.Data, "wsm-deployment-marker", namespace); err != nil {
		return fmt.Errorf("failed to update deployment marker: %w", err)
	}
	return nil
}

// FindNamespacesWithMarker finds all namespaces containing the wsm-deployment-marker for a specific component
func FindNamespacesWithMarker(ctx context.Context, component string) ([]string, error) {
	cms, err := ListConfigMaps(ctx, "wsm-deployment-marker")
	if err != nil {
		return nil, fmt.Errorf("failed to list deployment markers: %w", err)
	}

	var namespaces []string
	for _, cm := range cms {
		if components, ok := cm.Data["components"]; ok {
			for c := range strings.SplitSeq(components, ",") {
				if strings.TrimSpace(c) == component {
					namespaces = append(namespaces, cm.Namespace)
					break
				}
			}
		}
	}

	return namespaces, nil
}
