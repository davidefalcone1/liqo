package authservice

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"

	"github.com/julienschmidt/httprouter"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog"

	"github.com/liqotech/liqo/pkg/auth"
)

func (authService *Controller) role(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	bytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	roleRequest := auth.ServiceAccountIdentityRequest{}
	err = json.Unmarshal(bytes, &roleRequest)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	// check that the provided credentials are valid
	klog.V(4).Info("Checking credentials")
	if err = authService.credentialsValidator.checkCredentials(
		&roleRequest, authService.getConfigProvider(), authService.getTokenManager()); err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	klog.Infof("Create ServiceAccount remote-%v", roleRequest.ClusterID)
	sa, err := authService.createServiceAccount(roleRequest.ClusterID)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	klog.Infof("Create Role %v", sa.Name)
	role, err := authService.createRole(roleRequest.ClusterID, sa)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	klog.Infof("Create RoleBinding %v", sa.Name)
	_, err = authService.createRoleBinding(sa, role, roleRequest.ClusterID)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	klog.Infof("Create ClusterRole %v", sa.Name)
	clusterRole, err := authService.createClusterRole(roleRequest.ClusterID, sa)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	klog.Infof("Create ClusterRoleBinding %v", sa.Name)
	_, err = authService.createClusterRoleBinding(sa, clusterRole, roleRequest.ClusterID)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}

	klog.Infof("Wait for complete ServiceAccount remote-%v", roleRequest.ClusterID)
	sa, err = authService.getServiceAccountCompleted(roleRequest.ClusterID)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}
	klog.Infof("ServiceAccount remote-%v Ready", sa.Name)

	kubeconfig, err := authService.createKubeConfig(sa)
	if err != nil {
		klog.Error(err)
		authService.handleError(w, err)
		return
	}
	klog.V(8).Infof("Kubeconfig created: %v", kubeconfig)

	w.WriteHeader(http.StatusCreated)
	_, err = w.Write([]byte(kubeconfig))
	if err != nil {
		klog.Error(err)
		return
	}
}

func (authService *Controller) handleError(w http.ResponseWriter, err error) {
	if errors.Is(err, &kerrors.StatusError{}) {
		authService.sendError(w, "forbidden", http.StatusForbidden)
	} else {
		authService.sendError(w, err.Error(), http.StatusInternalServerError)
	}
}

func (authService *Controller) sendError(w http.ResponseWriter, resp interface{}, code int) {
	bytes := []byte{}
	var err error
	if resp != nil {
		bytes, err = json.Marshal(resp)
		if err != nil {
			klog.Error(err)
			return
		}
	}
	http.Error(w, string(bytes), code)
}
