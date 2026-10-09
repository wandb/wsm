package supportbundle

import (
	v2 "github.com/wandb/operator/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func secretValue(name, key string) v2.ValueOrSecret {
	return v2.ValueOrSecret{ValueFrom: &v2.SecretValueSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key,
	}}}
}

func testCR(telemetryMode string) *v2.WeightsAndBiases {
	cr := &v2.WeightsAndBiases{
		ObjectMeta: metav1.ObjectMeta{Name: "wandb", Namespace: "wandb"},
	}
	cr.Spec.Global.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}
	cr.Spec.Wandb.ServiceAccount.Annotations = map[string]string{
		"eks.amazonaws.com/role-arn": "arn:aws:iam::123:role/wandb",
		"unrelated":                  "x",
	}
	cr.Status.Ready = true
	cr.Status.ObjectStoreStatus = map[string]v2.ObjectStoreInfraStatus{
		"default": {
			WBInfraStatus: v2.WBInfraStatus{Ready: true},
			Connection: v2.ObjectStoreConnection{
				URL:       secretValue("wandb-objectstore-connection", "url"),
				AccessKey: secretValue("wandb-objectstore-connection", "AccessKey"),
				SecretKey: secretValue("wandb-objectstore-connection", "SecretKey"),
				Region:    secretValue("wandb-objectstore-connection", "Region"),
				Bucket:    secretValue("wandb-objectstore-connection", "Bucket"),
			},
		},
	}
	if telemetryMode != "" {
		cr.Status.TelemetryStatus = v2.TelemetryInfraStatus{
			WBInfraStatus: v2.WBInfraStatus{Ready: true},
			Mode:          telemetryMode,
			Connection: v2.TelemetryConnectionStatus{
				ManagedNamespace:    "wandb-telemetry",
				MetricsReadEndpoint: "http://vmsingle-victoria-instance.wandb-telemetry.svc:8428",
				LogsReadEndpoint:    "http://vlsingle-victoria-logs.wandb-telemetry.svc:9428",
			},
		}
	}
	return cr
}

func testInstallation(telemetryMode string) *Installation {
	inst, _, err := FromCR(testCR(telemetryMode))
	if err != nil {
		panic(err)
	}
	return inst
}
