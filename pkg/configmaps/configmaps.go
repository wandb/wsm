package configmaps

import (
	"context"

	"github.com/wandb/wsm/pkg/kubectl"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func UpsertConfigMap(data map[string]string, name string, namespace string) error {
	ctx := context.Background()
	_, cs, err := kubectl.GetClientset()
	if err != nil {
		panic(err)
	}

	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: data,
	}

	_, err = cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		_, err = cs.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		return nil
	}

	_, err = cs.CoreV1().ConfigMaps(namespace).Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}
