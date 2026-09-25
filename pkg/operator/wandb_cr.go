package operator

import (
	"fmt"
	"strings"
	"time"

	v2 "github.com/wandb/operator/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"knative.dev/pkg/ptr"
)

// SecurityFlags are the spec.wandb.security toggles; a nil field is left unset.
type SecurityFlags struct {
	AllowUserTeamCreation          *bool
	DisableCodeSaving              *bool
	AllowAnonymousPublicProjects   *bool
	DisableSSOProvisioning         *bool
	InsecureAllowAPIKeyAdminAccess *bool
	HideUpgradeBanner              *bool
}

// SetSecurity applies the toggles to spec.wandb.security.
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

// SetRetention configures spec.wandb.retention; no-op when both args are unset.
func SetRetention(cr *v2.WeightsAndBiases, artifactGC *bool, dataRetentionPeriod string) error {
	if artifactGC == nil && dataRetentionPeriod == "" {
		return nil
	}
	r := cr.Spec.Wandb.Retention
	if r == nil {
		r = &v2.RetentionSpec{}
	}
	if artifactGC != nil {
		r.ArtifactGarbageCollection = *artifactGC
	}
	if dataRetentionPeriod != "" {
		d, err := time.ParseDuration(dataRetentionPeriod)
		if err != nil {
			return fmt.Errorf("--data-retention-period must be a duration like 720h (units: h, m, s): %w", err)
		}
		r.DataRetentionPeriod = &metav1.Duration{Duration: d}
	}
	cr.Spec.Wandb.Retention = r
	return nil
}

// ParseValueOrSecret turns a literal or a "<name>:<key>" secret ref into a
// v2.ValueOrSecret. Both empty yields the zero value; both set is an error.
func ParseValueOrSecret(literal, secretRef string) (v2.ValueOrSecret, error) {
	if literal != "" && secretRef != "" {
		return v2.ValueOrSecret{}, fmt.Errorf("a literal value and a secret ref are mutually exclusive")
	}
	if literal != "" {
		return v2.LiteralValue(literal), nil
	}
	if secretRef == "" {
		return v2.ValueOrSecret{}, nil
	}
	name, key, ok := strings.Cut(secretRef, ":")
	if !ok || name == "" || key == "" {
		return v2.ValueOrSecret{}, fmt.Errorf("secret ref must be <secret-name>:<key>, got %q", secretRef)
	}
	return v2.ValueFromSelector(corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}), nil
}

// EmailInputs and SlackInputs carry notification flag values. Sink, SMTPPassword
// and ClientSecret take a "<name>:<key>" secret ref; the rest are literals.
type EmailInputs struct {
	Sink         string
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
}

type SlackInputs struct {
	ClientID     string
	ClientSecret string
}

// MergeNotifications overlays notification flags onto an existing CR-file
// value. Unset flag groups and leaves are preserved; email accepts a sink or
// SMTP, not both.
func MergeNotifications(existing *v2.NotificationsSpec, email EmailInputs, slack SlackInputs) (*v2.NotificationsSpec, error) {
	n := existing.DeepCopy()
	if n == nil {
		n = &v2.NotificationsSpec{}
	}

	if err := mergeEmail(n, email); err != nil {
		return nil, err
	}
	if err := mergeSlack(n, slack); err != nil {
		return nil, err
	}
	if n.Email == nil && n.Slack == nil {
		return nil, nil
	}
	return n, nil
}

func mergeEmail(n *v2.NotificationsSpec, in EmailInputs) error {
	smtpSet := in.SMTPHost != "" || in.SMTPPort != "" || in.SMTPUsername != "" || in.SMTPPassword != ""
	if in.Sink != "" && smtpSet {
		return fmt.Errorf("--email-sink and --smtp-* are mutually exclusive")
	}
	if in.Sink != "" {
		sink, err := ParseValueOrSecret("", in.Sink)
		if err != nil {
			return err
		}
		n.Email = &v2.EmailSpec{Sink: &sink}
		return nil
	}
	if !smtpSet {
		return nil
	}

	// Preserve SMTP leaves supplied by --cr-file, but switch away from an
	// existing sink when any SMTP flag explicitly selects the SMTP arm.
	smtp := &v2.EmailSMTPSpec{}
	if n.Email != nil && n.Email.SMTP != nil {
		smtp = n.Email.SMTP.DeepCopy()
	}
	if in.SMTPHost != "" {
		smtp.Host = v2.LiteralValue(in.SMTPHost)
	}
	if in.SMTPPort != "" {
		smtp.Port = v2.LiteralValue(in.SMTPPort)
	}
	if in.SMTPUsername != "" {
		smtp.Username = v2.LiteralValue(in.SMTPUsername)
	}
	if in.SMTPPassword != "" {
		password, err := ParseValueOrSecret("", in.SMTPPassword)
		if err != nil {
			return err
		}
		smtp.Password = password
	}
	// The webhook requires all four SMTP fields after flags and --cr-file merge.
	if smtp.Host.IsZero() || smtp.Port.IsZero() || smtp.Username.IsZero() || smtp.Password.IsZero() {
		return fmt.Errorf("SMTP email needs host, port, username, and password from --smtp-* flags or --cr-file")
	}
	n.Email = &v2.EmailSpec{SMTP: smtp}
	return nil
}

func mergeSlack(n *v2.NotificationsSpec, in SlackInputs) error {
	if in.ClientID == "" && in.ClientSecret == "" {
		return nil
	}
	slack := &v2.SlackSpec{}
	if n.Slack != nil {
		slack = n.Slack.DeepCopy()
	}
	if in.ClientID != "" {
		slack.ClientID = v2.LiteralValue(in.ClientID)
	}
	if in.ClientSecret != "" {
		secret, err := ParseValueOrSecret("", in.ClientSecret)
		if err != nil {
			return err
		}
		slack.ClientSecret = secret
	}
	// The webhook requires both Slack fields after flags and --cr-file merge.
	if slack.ClientID.IsZero() || slack.ClientSecret.IsZero() {
		return fmt.Errorf("slack needs client ID and client secret from --slack-* flags or --cr-file")
	}
	n.Slack = slack
	return nil
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

// DefaultWandbCR returns the base CR wsm deploys: managed infra under the
// default instance, telemetry off. Callers set name/namespace/hostname/version.
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
