package kubectl

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetSecret(name string, namespace string) ([]byte, error) {
	return GetSecretData(name, namespace, "password")
}

func GetSecretData(name string, namespace string, key string) ([]byte, error) {
	secretData, err := GetSecretDataMap(name, namespace)
	if err != nil {
		return nil, err
	}

	value, ok := secretData[key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain key %q", namespace, name, key)
	}

	return value, nil
}

func GetSecretDataMap(name string, namespace string) (map[string][]byte, error) {
	_, cs, err := GetClientset()
	if err != nil {
		return nil, err
	}

	secret, err := cs.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return secret.Data, nil
}
