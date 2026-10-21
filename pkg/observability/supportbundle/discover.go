package supportbundle

import (
	"context"
	"fmt"
	"strings"

	v2 "github.com/wandb/operator/api/v2"
	"github.com/wandb/wsm/pkg/operator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Discover returns the single WeightsAndBiases CR matching the optional
// namespace and name. Both are only needed when the cluster is ambiguous.
func Discover(ctx context.Context, namespace, name string) (*v2.WeightsAndBiases, error) {
	// Fully named: skip the List, which a user may not be allowed to do.
	if namespace != "" && name != "" {
		return operator.GetCR(ctx, name, namespace)
	}
	var refs []operator.CRRef
	if namespace != "" {
		names, err := operator.ListCRs(ctx, namespace)
		if err != nil {
			return nil, discoveryError(err)
		}
		for _, n := range names {
			refs = append(refs, operator.CRRef{Namespace: namespace, Name: n})
		}
	} else {
		all, err := operator.ListAllCRs(ctx)
		if err != nil {
			return nil, discoveryError(err)
		}
		refs = all
	}
	ref, err := SelectInstallation(refs, namespace, name)
	if err != nil {
		return nil, err
	}
	return operator.GetCR(ctx, ref.Name, ref.Namespace)
}

func discoveryError(err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("cannot list WeightsAndBiases to find the installation - pass --wandb-namespace and --wandb-name: %w", err)
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("the v2 WeightsAndBiases CRD is not installed in this cluster: %w", err)
	}
	return err
}

// SelectInstallation picks the one CR matching the optional namespace/name.
func SelectInstallation(refs []operator.CRRef, namespace, name string) (operator.CRRef, error) {
	var matches []operator.CRRef
	for _, r := range refs {
		if (namespace == "" || r.Namespace == namespace) && (name == "" || r.Name == name) {
			matches = append(matches, r)
		}
	}
	scope := "in the cluster"
	if namespace != "" {
		scope = "in namespace " + namespace
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if name != "" {
			return operator.CRRef{}, fmt.Errorf("no v2 WeightsAndBiases named %q found %s", name, scope)
		}
		return operator.CRRef{}, fmt.Errorf("no v2 WeightsAndBiases found %s", scope)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Namespace + "/" + m.Name
		}
		return operator.CRRef{}, fmt.Errorf("found %d WeightsAndBiases %s (%s) - select one with --wandb-namespace and --wandb-name",
			len(matches), scope, strings.Join(names, ", "))
	}
}
