package identitymanager

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/liqotech/liqo/pkg/auth"
)

// IdentityManager interface provides the methods to manage identities for the remote clusters.
type IdentityManager interface {
	localManager
	remoteManager
}

// interface that allows to manage the identity in the owner cluster.
type localManager interface {
	CreateIdentity(remoteClusterID string) (*v1.Secret, error)
	GetSigningRequest(remoteClusterID string) ([]byte, error)
	StoreCertificate(remoteClusterID string, identityResponse auth.CertificateIdentityResponse) error

	GetConfig(remoteClusterID string, namespace string) (*rest.Config, error)
	GetRemoteTenantNamespace(remoteClusterID string, namespace string) (string, error)
}

// interface that allows to manage the identity in the target cluster, where this identity has to be used.
type remoteManager interface {
	ApproveSigningRequest(clusterID, signingRequest string) (certificate []byte, err error)
	GetRemoteCertificate(clusterID, signingRequest string) (certificate []byte, err error)
}
