package kubectl

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
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

// AddDeploymentMarker records a single component in the marker, creating it if
// absent and preserving components already listed. It lets a component claim
// ownership the moment it's installed, so a later step failing can't strand it
// unmarked. The read-merge-write retries on conflict, re-reading current components
// each attempt, so concurrent writers don't drop each other's entries; the Update
// targets the fetched object's resourceVersion, which is what surfaces a conflict.
func AddDeploymentMarker(ctx context.Context, namespace, component string) error {
	_, cs, err := GetClientset()
	if err != nil {
		return err
	}
	cms := cs.CoreV1().ConfigMaps(namespace)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := cms.Get(ctx, "wsm-deployment-marker", metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return CreateDeploymentMarker(ctx, "", namespace, component)
			}
			return fmt.Errorf("failed to check for deployment marker: %w", err)
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		existing := cm.Data["components"]
		for c := range strings.SplitSeq(existing, ",") {
			if strings.TrimSpace(c) == component {
				return nil
			}
		}
		if existing == "" {
			cm.Data["components"] = component
		} else {
			cm.Data["components"] = existing + "," + component
		}
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
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
