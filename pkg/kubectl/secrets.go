package kubectl

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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

// SecretReader reads ValueOrSecret-style Secret keys from one namespace with the
// operator's semantics (internal/controller/infra/external.ResolveSecretKey): a
// missing Secret is always an error, a missing key resolves to "" when the
// selector is optional. Each Secret is fetched once, since a connection's fields
// usually share one. The clientset is resolved on first use, so callers whose
// values are all literals never need a cluster.
//
// Not safe for concurrent use, and the cache never expires: create one per run.
type SecretReader struct {
	namespace string
	client    kubernetes.Interface
	cache     map[string]map[string][]byte
}

// NewSecretReader uses wsm's context-aware clientset on first Get.
func NewSecretReader(namespace string) *SecretReader {
	return &SecretReader{namespace: namespace, cache: map[string]map[string][]byte{}}
}

// NewSecretReaderWithClient is for tests and callers that already hold a clientset.
func NewSecretReaderWithClient(namespace string, client kubernetes.Interface) *SecretReader {
	r := NewSecretReader(namespace)
	r.client = client
	return r
}

func (r *SecretReader) Get(ctx context.Context, sel corev1.SecretKeySelector) (string, error) {
	if r.namespace == "" {
		return "", fmt.Errorf("secret %s is referenced but no namespace is set", sel.Name)
	}
	data, ok := r.cache[sel.Name]
	if !ok {
		if r.client == nil {
			_, cs, err := GetClientset()
			if err != nil {
				return "", fmt.Errorf("not connected to cluster: %w", err)
			}
			r.client = cs
		}
		secret, err := r.client.CoreV1().Secrets(r.namespace).Get(ctx, sel.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("read secret %s/%s: %w", r.namespace, sel.Name, err)
		}
		data = secret.Data
		r.cache[sel.Name] = data
	}
	value, ok := data[sel.Key]
	if !ok {
		if sel.Optional != nil && *sel.Optional {
			return "", nil
		}
		return "", fmt.Errorf("secret %s/%s has no key %q", r.namespace, sel.Name, sel.Key)
	}
	return string(value), nil
}
