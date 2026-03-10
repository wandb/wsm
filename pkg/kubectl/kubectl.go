package kubectl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	config      *rest.Config
	clientset   *kubernetes.Clientset
	dynamicHost *dynamic.DynamicClient
	mapper      meta.RESTMapper
	initErr     error
	mapperErr   error
	once        sync.Once
	mapperOnce  sync.Once
	kubeContext string
)

func SetContext(ctx string) {
	kubeContext = ctx
}

func initMapper() {
	mapperOnce.Do(func() {
		if clientset == nil {
			mapperErr = fmt.Errorf("cannot initialize REST mapper: clientset is nil")
			return
		}
		gr, err := restmapper.GetAPIGroupResources(clientset.Discovery())
		if err != nil {
			mapperErr = fmt.Errorf("failed to discover API resources (is the cluster reachable?): %w", err)
			return
		}
		mapper = restmapper.NewDiscoveryRESTMapper(gr)
	})
}

func initConfig() {
	once.Do(func() {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		if kubeContext != "" {
			configOverrides.CurrentContext = kubeContext
		}

		var err error
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
		if err != nil {
			if kubeContext != "" {
				// Don't fall back to in-cluster config when a specific context was requested
				initErr = fmt.Errorf("failed to load kubeconfig (context=%q): %w", kubeContext, err)
				return
			}
			// fallback to in-cluster config only when no context is specified
			config, err = rest.InClusterConfig()
			if err != nil {
				initErr = fmt.Errorf("failed to load kubeconfig: %w", err)
				return
			}
		}

		var csErr error
		clientset, csErr = kubernetes.NewForConfig(config)
		if csErr != nil {
			initErr = fmt.Errorf("failed to create kubernetes clientset: %w", csErr)
			return
		}

		dynamicHost, err = dynamic.NewForConfig(config)
		if err != nil {
			initErr = fmt.Errorf("failed to create dynamic client: %w", err)
			return
		}

		initMapper()
	})
}

func GetConfig() (*rest.Config, error) {
	initConfig()
	if initErr != nil {
		return nil, initErr
	}
	if config == nil {
		return nil, fmt.Errorf("kubernetes config not initialized")
	}
	return config, nil
}

func GetDynamicClientset() (*rest.Config, *dynamic.DynamicClient, error) {
	initConfig()
	if initErr != nil {
		return nil, nil, initErr
	}
	if config == nil || dynamicHost == nil {
		return nil, nil, fmt.Errorf("kubernetes client not initialized")
	}
	return config, dynamicHost, nil
}

func GetClientset() (*rest.Config, *kubernetes.Clientset, error) {
	initConfig()
	if initErr != nil {
		return nil, nil, initErr
	}
	if config == nil || clientset == nil {
		return nil, nil, fmt.Errorf("kubernetes client not initialized")
	}
	return config, clientset, nil
}

func GetRESTMapper() (meta.RESTMapper, error) {
	initConfig()
	if initErr != nil {
		return nil, initErr
	}
	initMapper()
	if mapperErr != nil {
		return nil, mapperErr
	}
	if mapper == nil {
		return nil, fmt.Errorf("REST mapper not initialized")
	}
	return mapper, nil
}

func RefreshRESTMapper() (meta.RESTMapper, error) {
	initConfig()
	if initErr != nil {
		return nil, initErr
	}
	if clientset == nil {
		return nil, fmt.Errorf("cannot refresh REST mapper: clientset is nil")
	}
	gr, err := restmapper.GetAPIGroupResources(clientset.Discovery())
	if err != nil {
		return nil, fmt.Errorf("failed to discover API resources: %w", err)
	}
	mapper = restmapper.NewDiscoveryRESTMapper(gr)
	return mapper, nil
}

func IsConnectedToCluster() bool {
	_, _, err := GetClientset()
	return err == nil
}

func ApplyYAML(ctx context.Context, yamlContent []byte) error {
	_, dyn, err := GetDynamicClientset()
	if err != nil {
		return err
	}

	mapper, err := GetRESTMapper()
	if err != nil {
		return err
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(yamlContent), 4096)
	for {
		var rawObj runtime.RawExtension
		if err := decoder.Decode(&rawObj); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode manifest: %w", err)
		}

		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(rawObj.Raw); err != nil {
			return fmt.Errorf("failed to unmarshal manifest object: %w", err)
		}

		if obj.Object == nil {
			continue
		}

		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			// If mapping fails, try refreshing the mapper as CRDs might have been just installed
			if refreshedMapper, refreshErr := RefreshRESTMapper(); refreshErr == nil {
				mapping, err = refreshedMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			}

			if err != nil {
				return fmt.Errorf("failed to get mapping for %s: %w", gvk, err)
			}
		}

		var dr dynamic.ResourceInterface
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			dr = dyn.Resource(mapping.Resource).Namespace(obj.GetNamespace())
		} else {
			dr = dyn.Resource(mapping.Resource)
		}

		data, err := obj.MarshalJSON()
		if err != nil {
			return err
		}

		_, err = dr.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: "wsm",
		})
		if err != nil {
			return fmt.Errorf("failed to apply object %s %s/%s: %w", gvk, obj.GetNamespace(), obj.GetName(), err)
		}
	}

	return nil
}

func PatchDeployment(ctx context.Context, name, namespace string, patchType types.PatchType, patch []byte) error {
	_, cs, err := GetClientset()
	if err != nil {
		return err
	}

	_, err = cs.AppsV1().Deployments(namespace).Patch(ctx, name, patchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch deployment %s/%s: %w", namespace, name, err)
	}
	return nil
}

func DeleteYAML(ctx context.Context, yamlContent []byte) error {
	_, dyn, err := GetDynamicClientset()
	if err != nil {
		return err
	}

	mapper, err := GetRESTMapper()
	if err != nil {
		return err
	}

	// We should decode in reverse order to delete dependencies correctly?
	// Actually, just regular order might be fine, but we'll see.
	// K8s usually handles dependencies via owner references, but here they might not have them.

	var objects []*unstructured.Unstructured
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(yamlContent), 4096)
	for {
		var rawObj runtime.RawExtension
		if err := decoder.Decode(&rawObj); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode manifest: %w", err)
		}

		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(rawObj.Raw); err != nil {
			return fmt.Errorf("failed to unmarshal manifest object: %w", err)
		}

		if obj.Object == nil {
			continue
		}
		objects = append(objects, obj)
	}

	// Delete in reverse order
	for i := len(objects) - 1; i >= 0; i-- {
		obj := objects[i]
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			// If mapping fails, just skip (might have been deleted already)
			continue
		}

		var dr dynamic.ResourceInterface
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			dr = dyn.Resource(mapping.Resource).Namespace(obj.GetNamespace())
		} else {
			dr = dyn.Resource(mapping.Resource)
		}

		err = dr.Delete(ctx, obj.GetName(), metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete object %s %s/%s: %w", gvk, obj.GetNamespace(), obj.GetName(), err)
		}
	}

	return nil
}

func DeleteCR(ctx context.Context, name, namespace string) error {
	gvk := schema.GroupVersionKind{
		Group:   "apps.wandb.com",
		Version: "v2",
		Kind:    "WeightsAndBiases",
	}
	_, dyn, err := GetDynamicClientset()
	if err != nil {
		return err
	}

	mapper, err := GetRESTMapper()
	if err != nil {
		return err
	}

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to get mapping for %s: %w", gvk, err)
	}

	var dr dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if namespace == "" {
			return fmt.Errorf("namespace is required for namespaced resource %s", gvk)
		}
		dr = dyn.Resource(mapping.Resource).Namespace(namespace)
	} else {
		dr = dyn.Resource(mapping.Resource)
	}

	err = dr.Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object %s %s/%s: %w", gvk, namespace, name, err)
	}

	return nil
}
