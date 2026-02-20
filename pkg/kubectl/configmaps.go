package kubectl

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetConfigMap(ctx context.Context, name, namespace string) (*v1.ConfigMap, error) {
	_, cs, err := GetClientset()
	if err != nil {
		return nil, err
	}

	cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func DeleteConfigMap(ctx context.Context, name, namespace string) error {
	_, cs, err := GetClientset()
	if err != nil {
		return err
	}

	err = cs.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func UpsertConfigMap(data map[string]string, name string, namespace string) error {
	ctx := context.Background()
	_, cs, err := GetClientset() // Assuming GetClientset() returns the context and clientset
	if err != nil {
		return fmt.Errorf("failed to get clientset: %w", err)
	}

	// Try to get the existing ConfigMap
	existingConfigMap, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// If the ConfigMap doesn't exist, create it
		if errors.IsNotFound(err) {
			configMap := &v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    map[string]string{"app.kubernetes.io/managed-by": "wsm"},
				},
				Data: data,
			}

			_, err = cs.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create ConfigMap: %w", err)
			}
			return nil
		}
		// If there was another error, return it
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// If the ConfigMap exists, update it
	existingConfigMap.Data = data
	_, err = cs.CoreV1().ConfigMaps(namespace).Update(ctx, existingConfigMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update ConfigMap: %w", err)
	}

	return nil
}
