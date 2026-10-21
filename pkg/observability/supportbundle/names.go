package supportbundle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	v2 "github.com/wandb/operator/api/v2"
)

// Labels wsm puts on manager Jobs. The component differs from the operator's
// so a manager Job never counts as a running collection.
const (
	ManagedByLabel   = "app.kubernetes.io/managed-by"
	ComponentLabel   = "app.kubernetes.io/component"
	PartOfLabel      = "app.kubernetes.io/part-of"
	OperationLabel   = "support-bundle.wsm.wandb.ai/operation"
	ManagerComponent = "support-bundle-manager"
)

// Operation is the bundle-manager subcommand a manager Job serves.
type Operation string

const (
	OperationList     Operation = "list"
	OperationRetrieve Operation = "retrieve"
	OperationDelete   Operation = "delete"
)

func ManagerJobName(op Operation, suffix string) string {
	return fmt.Sprintf("support-bundle-%s-%s", op, suffix)
}

// NewSuffix returns six random hex characters for a unique Job name.
func NewSuffix() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate job name: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func ManagerLabels(cr *v2.WeightsAndBiases, op Operation) map[string]string {
	return map[string]string{
		ManagedByLabel:      "wsm",
		ComponentLabel:      ManagerComponent,
		PartOfLabel:         "wandb",
		WandbNameLabel:      cr.Name,
		WandbNamespaceLabel: cr.Namespace,
		OperationLabel:      string(op),
	}
}
