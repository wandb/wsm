package kubectl

import (
	"context"
	"fmt"

	"github.com/pingcap/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

// func UpsertConfigMap(data map[string]string, name string, namespace string) error {
// 	ctx := context.Background()
// 	_, cs, err := GetClientset()
// 	if err != nil {
// 		panic(err)
// 	}

// 	configMap := &v1.ConfigMap{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name: name,
// 		},
// 		Data: data,
// 	}

// 	_, err = cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
// 	if err != nil {
// 		_, err = cs.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
// 		if err != nil {
// 			return err
// 		}
// 		return nil
// 	}

// 	_, err = cs.CoreV1().ConfigMaps(namespace).Update(ctx, configMap, metav1.UpdateOptions{})
// 	if err != nil {
// 		return err
// 	}

// 	return nil
// }
