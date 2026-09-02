package operator

import (
	"fmt"
	"strings"

	v2 "github.com/wandb/operator/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"knative.dev/pkg/ptr"
)

// SecurityFlags holds the spec.wandb.security toggles. A nil field is left
// unset so the operator default applies.
type SecurityFlags struct {
	AllowUserTeamCreation          *bool
	DisableCodeSaving              *bool
	AllowAnonymousPublicProjects   *bool
	DisableSSOProvisioning         *bool
	InsecureAllowAPIKeyAdminAccess *bool
	HideUpgradeBanner              *bool
}

// SetSecurity applies the provided security toggles to spec.wandb.security.
func SetSecurity(cr *v2.WeightsAndBiases, f SecurityFlags) {
	s := &cr.Spec.Wandb.Security
	if f.AllowUserTeamCreation != nil {
		s.AllowUserTeamCreation = *f.AllowUserTeamCreation
	}
	if f.DisableCodeSaving != nil {
		s.DisableCodeSaving = *f.DisableCodeSaving
	}
	if f.AllowAnonymousPublicProjects != nil {
		s.AllowAnonymousPublicProjects = *f.AllowAnonymousPublicProjects
	}
	if f.DisableSSOProvisioning != nil {
		s.DisableSSOProvisioning = *f.DisableSSOProvisioning
	}
	if f.InsecureAllowAPIKeyAdminAccess != nil {
		s.InsecureAllowAPIKeyAdminAccess = *f.InsecureAllowAPIKeyAdminAccess
	}
	if f.HideUpgradeBanner != nil {
		s.HideUpgradeBanner = *f.HideUpgradeBanner
	}
}

// ValidateImagePullSecretNames rejects values that cannot name a Kubernetes
// Secret. All names are checked before callers mutate a CR.
func ValidateImagePullSecretNames(names []string) error {
	for _, name := range names {
		if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
			return fmt.Errorf("--image-pull-secret %q is not a valid Kubernetes Secret name: %s", name, strings.Join(problems, "; "))
		}
	}
	return nil
}

// SetImagePullSecrets validates every name before appending any Secret refs to
// spec.global.imagePullSecrets.
func SetImagePullSecrets(cr *v2.WeightsAndBiases, names []string) error {
	if err := ValidateImagePullSecretNames(names); err != nil {
		return err
	}
	for _, name := range names {
		cr.Spec.Global.ImagePullSecrets = append(cr.Spec.Global.ImagePullSecrets, corev1.LocalObjectReference{Name: name})
	}
	return nil
}

// DefaultWandbCR returns the base WeightsAndBiases wsm deploys: managed infra
// keyed under the reserved default instance, telemetry off. Callers fill in
// name, namespace, hostname, version and size before applying.
func DefaultWandbCR() *v2.WeightsAndBiases {
	return &v2.WeightsAndBiases{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.wandb.com/v2",
			Kind:       "WeightsAndBiases",
		},
		Spec: v2.WeightsAndBiasesSpec{
			AdminConsoleEnabled: ptr.Bool(true),
			Wandb: v2.WandbAppSpec{
				Hostname: "http://localhost:8080",
				Features: map[string]bool{},
				InternalServiceAuth: v2.InternalServiceAuth{
					Enabled: ptr.Bool(false),
				},
			},
			MySQL: map[string]v2.MySQLSpec{
				v2.DefaultInstanceName: {
					ManagedMysql: &v2.ManagedMysqlSpec{
						Telemetry: v2.Telemetry{Enabled: false},
					},
				},
			},
			Redis: map[string]v2.RedisSpec{
				v2.DefaultInstanceName: {
					ManagedRedis: &v2.ManagedRedisSpec{
						Telemetry: v2.Telemetry{Enabled: false},
					},
				},
			},
			Kafka: v2.KafkaSpec{
				ManagedKafka: &v2.ManagedKafkaSpec{
					Telemetry: v2.Telemetry{Enabled: false},
				},
			},
			ObjectStore: map[string]v2.ObjectStoreSpec{
				v2.DefaultInstanceName: {
					ManagedObjectStore: &v2.ManagedObjectStoreSpec{
						Telemetry: v2.Telemetry{Enabled: false},
					},
				},
			},
			ClickHouse: map[string]v2.ClickHouseSpec{
				v2.DefaultInstanceName: {
					ManagedClickHouse: &v2.ManagedClickHouseSpec{
						Telemetry: v2.Telemetry{Enabled: false},
					},
				},
			},
		},
	}
}
