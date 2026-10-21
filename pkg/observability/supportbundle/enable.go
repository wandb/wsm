package supportbundle

import (
	"context"
	"fmt"

	"github.com/wandb/wsm/pkg/operator"
)

// SetEnabled patches only spec.supportBundle.enabled, leaving fields other
// managers own untouched.
func SetEnabled(ctx context.Context, name, namespace string, enabled bool) error {
	patch := fmt.Sprintf(`{"spec":{"supportBundle":{"enabled":%t}}}`, enabled)
	return operator.MergePatchCR(ctx, name, namespace, []byte(patch))
}
