package kubectl

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetClusterRole(name string) (*rbacv1.ClusterRole, error) {
	ctx := context.Background()
	_, cs, err := GetClientset()
	if err != nil {
		return nil, err
	}

	clusterRole, err := cs.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return clusterRole, nil
}

func GetClusterRoleBinding(name string) (*rbacv1.ClusterRoleBinding, error) {
	ctx := context.Background()
	_, cs, err := GetClientset()
	if err != nil {
		return nil, err
	}

	clusterRoleBinding, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return clusterRoleBinding, nil
}

func DeleteClusterRole(name string) error {
	ctx := context.Background()
	_, cs, err := GetClientset()
	if err != nil {
		return err
	}

	return cs.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
}

func DeleteClusterRoleBinding(name string) error {
	ctx := context.Background()
	_, cs, err := GetClientset()
	if err != nil {
		return err
	}

	return cs.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
}
