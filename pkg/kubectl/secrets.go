package kubectl

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetSecret(name string, namespace string) ([]byte, error) {
	_, cs, err := GetClientset()
	if err != nil {
		return nil, err
	}

	secret, err := cs.CoreV1().Secrets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return secret.Data["password"], nil
}
