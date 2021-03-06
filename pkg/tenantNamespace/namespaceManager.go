package tenantnamespace

import (
	"context"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/liqotech/liqo/pkg/discovery"
)

type tenantNamespaceManager struct {
	client kubernetes.Interface
}

// NewTenantNamespaceManager creates a new TenantNamespaceManager object.
func NewTenantNamespaceManager(client kubernetes.Interface) Manager {
	return &tenantNamespaceManager{
		client: client,
	}
}

// CreateNamespace creates a new Tenant Namespace given the clusterid
// This method is idempotent, multiple calls of it will not lead to multiple namespace creations.
func (nm *tenantNamespaceManager) CreateNamespace(clusterID string) (*v1.Namespace, error) {
	// first check that it does not exist yet
	ns, err := nm.GetNamespace(clusterID)
	if err == nil {
		return ns, nil
	} else if !kerrors.IsNotFound(err) {
		// an error occurred, but it is not a not found error
		klog.Error(err)
		return nil, err
	}
	// a not found error occurred, create the namespace

	ns = &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: strings.Join([]string{tenantNamespaceRoot, ""}, "-"),
			Labels: map[string]string{
				discovery.ClusterIDLabel:       clusterID,
				discovery.TenantNamespaceLabel: "true",
			},
		},
	}
	if ns, err = nm.client.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{}); err != nil {
		klog.Error(err)
		return nil, err
	}
	klog.V(4).Infof("Namespace %v created for the remote cluster %v", ns.Name, clusterID)
	return ns, nil
}

// GetNamespace gets a Tenant Namespace given the clusterid.
func (nm *tenantNamespaceManager) GetNamespace(clusterID string) (*v1.Namespace, error) {
	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{
			discovery.ClusterIDLabel:       clusterID,
			discovery.TenantNamespaceLabel: "true",
		},
	}

	namespaces, err := nm.client.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	})
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	if nItems := len(namespaces.Items); nItems == 0 {
		err = kerrors.NewNotFound(v1.Resource("Namespace"), clusterID)
		// do not log it always, since it is also used in the preliminary stage of the create method
		klog.V(4).Info(err)
		return nil, err
	} else if nItems > 1 {
		err = fmt.Errorf("multiple tenant namespaces found for clusterid %v", clusterID)
		klog.Error(err)
		return nil, err
	}
	return &namespaces.Items[0], nil
}
