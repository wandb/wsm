package operator

import (
	v2 "github.com/wandb/operator/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"
)

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
