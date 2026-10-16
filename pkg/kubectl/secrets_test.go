package kubectl

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func selector(name, key string, optional bool) corev1.SecretKeySelector {
	return corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
		Optional:             &optional,
	}
}

func newTestReader() (*SecretReader, *fake.Clientset) {
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mysql", Namespace: "wandb"},
		Data:       map[string][]byte{"username": []byte("wandb"), "password": []byte("s3cret")},
	})
	return NewSecretReaderWithClient("wandb", client), client
}

func TestSecretReaderGet(t *testing.T) {
	tests := []struct {
		name    string
		sel     corev1.SecretKeySelector
		want    string
		wantErr string
	}{
		{name: "present key", sel: selector("mysql", "password", false), want: "s3cret"},
		{name: "missing required key", sel: selector("mysql", "Tls", false), wantErr: `has no key "Tls"`},
		{name: "missing optional key resolves empty", sel: selector("mysql", "Tls", true)},
		{name: "missing secret errors even when optional", sel: selector("absent", "Tls", true), wantErr: "read secret wandb/absent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newTestReader()
			got, err := r.Get(context.Background(), tt.sel)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Get = (%q, %v), want (%q, nil)", got, err, tt.want)
			}
		})
	}
}

func TestSecretReaderFetchesEachSecretOnce(t *testing.T) {
	r, client := newTestReader()
	for _, key := range []string{"username", "password"} {
		if _, err := r.Get(context.Background(), selector("mysql", key, false)); err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
	}
	gets := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "get" && a.GetResource().Resource == "secrets" {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("secret GETs = %d, want 1", gets)
	}
}

func TestSecretReaderWithoutNamespace(t *testing.T) {
	r := NewSecretReader("")
	if _, err := r.Get(context.Background(), selector("mysql", "password", false)); err == nil {
		t.Fatal("expected an error when no namespace is set")
	}
}
