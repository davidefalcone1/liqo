// Copyright © 2017 The virtual-kubelet authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package module

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pkgerrors "github.com/pkg/errors"
	coord "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	coordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// NodeProvider is the interface used for registering a node and updating its
// status in Kubernetes.
//
// Note: Implementers can choose to manage a node themselves, in which case
// it is not needed to provide an implementation for this interface.
type NodeProvider interface {
	// Ping checks if the node is still active.
	// This is intended to be lightweight as it will be called periodically as a
	// heartbeat to keep the node marked as ready in Kubernetes.
	Ping(context.Context) error

	// NotifyNodeStatus is used to asynchronously monitor the node.
	// The passed in callback should be called any time there is a change to the
	// node's status.
	// This will generally trigger a call to the Kubernetes API server to update
	// the status.
	//
	// NotifyNodeStatus should not block callers.
	NotifyNodeStatus(ctx context.Context, cb func(*corev1.Node))
}

// NewNodeController creates a new node controller.
// This does not have any side-effects on the system or kubernetes.
//
// Use the node's `Run` method to register and run the loops to update the node
// in Kubernetes.
//
// Note: When if there are multiple NodeControllerOpts which apply against the same
// underlying options, the last NodeControllerOpt will win.
func NewNodeController(p NodeProvider, node *corev1.Node, nodes v1.NodeInterface, opts ...NodeControllerOpt) (*NodeController, error) {
	n := &NodeController{p: p, n: node, nodes: nodes, chReady: make(chan struct{})}
	for _, o := range opts {
		if err := o(n); err != nil {
			return nil, pkgerrors.Wrap(err, "error applying node option")
		}
	}
	return n, nil
}

// NodeControllerOpt are the functional options used for configuring a node.
type NodeControllerOpt func(*NodeController) error

// WithNodeEnableLeaseV1 enables support for v1beta1 leases.
// If client is nil, leases will not be enabled.
// If baseLease is nil, a default base lease will be used.
//
// The lease will be updated after each successful node ping. To change the
// lease update interval, you must set the node ping interval.
// See WithNodePingInterval().
//
// This also affects the frequency of node status updates:
//   - When leases are *not* enabled (or are disabled due to no support on the cluster)
//     the node status is updated at every ping interval.
//   - When node leases are enabled, node status updates are controlled by the
//     node status update interval option.
// To set a custom node status update interval, see WithNodeStatusUpdateInterval().
func WithNodeEnableLeaseV1(client coordinationv1.LeaseInterface, baseLease *coord.Lease) NodeControllerOpt {
	return func(n *NodeController) error {
		n.leases = client
		n.lease = baseLease
		return nil
	}
}

// WithNodePingInterval sets the interval for checking node status
// If node leases are not supported (or not enabled), this is the frequency
// with which the node status will be updated in Kubernetes.
func WithNodePingInterval(d time.Duration) NodeControllerOpt {
	return func(n *NodeController) error {
		n.pingInterval = d
		return nil
	}
}

// WithNodeStatusUpdateInterval sets the interval for updating node status
// This is only used when leases are supported and only for updating the actual
// node status, not the node lease.
// When node leases are not enabled (or are not supported on the cluster) this
// has no affect and node status is updated on the "ping" interval.
func WithNodeStatusUpdateInterval(d time.Duration) NodeControllerOpt {
	return func(n *NodeController) error {
		n.statusInterval = d
		return nil
	}
}

// WithNodeStatusUpdateErrorHandler adds an error handler for cases where there is an error
// when updating the node status.
// This allows the caller to have some control on how errors are dealt with when
// updating a node's status.
//
// The error passed to the handler will be the error received from kubernetes
// when updating node status.
func WithNodeStatusUpdateErrorHandler(h ErrorHandler) NodeControllerOpt {
	return func(n *NodeController) error {
		n.nodeStatusUpdateErrorHandler = h
		return nil
	}
}

// ErrorHandler is a type of function used to allow callbacks for handling errors.
// It is expected that if a nil error is returned that the error is handled and
// progress can continue (or a retry is possible).
type ErrorHandler func(context.Context, error) error

// NodeController deals with creating and managing a node object in Kubernetes.
// It can register a node with Kubernetes and periodically update its status.
// NodeController manages a single node entity.
type NodeController struct {
	p NodeProvider
	n *corev1.Node

	leases coordinationv1.LeaseInterface
	nodes  v1.NodeInterface

	disableLease   bool
	pingInterval   time.Duration
	statusInterval time.Duration
	lease          *coord.Lease
	chStatusUpdate chan *corev1.Node

	nodeStatusUpdateErrorHandler ErrorHandler

	statusUpdateMutex sync.Mutex

	chReady chan struct{}
}

// The default intervals used for lease and status updates.
const (
	DefaultPingInterval         = 10 * time.Second
	DefaultStatusUpdateInterval = 1 * time.Minute
)

// Run registers the node in kubernetes and starts loops for updating the node
// status in Kubernetes.
//
// The node status must be updated periodically in Kubernetes to keep the node
// active. Newer versions of Kubernetes support node leases, which are
// essentially light weight pings. Older versions of Kubernetes require updating
// the node status periodically.
//
// If Kubernetes supports node leases this will use leases with a much slower
// node status update (because some things still expect the node to be updated
// periodically), otherwise it will only use node status update with the configured
// ping interval.
func (n *NodeController) Run(ctx context.Context) error {
	if n.pingInterval == time.Duration(0) {
		n.pingInterval = DefaultPingInterval
	}
	if n.statusInterval == time.Duration(0) {
		n.statusInterval = DefaultStatusUpdateInterval
	}

	n.chStatusUpdate = make(chan *corev1.Node)
	n.p.NotifyNodeStatus(ctx, func(node *corev1.Node) {
		n.chStatusUpdate <- node
	})

	if err := n.ensureNode(ctx); err != nil {
		return err
	}

	if n.leases == nil {
		n.disableLease = true
		return n.controlLoop(ctx)
	}

	n.lease = newLease(n.lease)
	setLeaseAttrs(n.lease, n.n, n.pingInterval*5)

	l, err := ensureLease(ctx, n.leases, n.lease)
	if err != nil {
		if !errors.IsNotFound(err) {
			return pkgerrors.Wrap(err, "error creating node lease")
		}
		klog.Info("Node leases not supported, falling back to only node status updates")
		n.disableLease = true
	}
	n.lease = l

	klog.V(4).Infof("Created lease for node s", n.n.Name)
	return n.controlLoop(ctx)
}

func (n *NodeController) ensureNode(ctx context.Context) error {
	err := n.updateStatus(ctx, true)
	if err == nil || !errors.IsNotFound(err) {
		return err
	}

	node, err := n.nodes.Create(context.TODO(), n.n, metav1.CreateOptions{})
	if err != nil {
		return pkgerrors.Wrap(err, "error registering node with kubernetes")
	}
	n.n = node

	return nil
}

// Ready returns a channel that gets closed when the node is fully up and
// running. Note that if there is an error on startup this channel will never
// be started.
func (n *NodeController) Ready() <-chan struct{} {
	return n.chReady
}

func (n *NodeController) controlLoop(ctx context.Context) error {
	pingTimer := time.NewTimer(n.pingInterval)
	defer pingTimer.Stop()

	statusTimer := time.NewTimer(n.statusInterval)
	defer statusTimer.Stop()
	timerResetDuration := n.statusInterval
	if n.disableLease {
		// when resetting the timer after processing a status update, reset it to the ping interval
		// (since it will be the ping timer as n.disableLease == true)
		timerResetDuration = n.pingInterval

		// hack to make sure this channel always blocks since we won't be using it
		if !statusTimer.Stop() {
			<-statusTimer.C
		}
	}

	close(n.chReady)

	for {
		select {
		case <-ctx.Done():
			return nil
		case updated := <-n.chStatusUpdate:
			var t *time.Timer
			if n.disableLease {
				t = pingTimer
			} else {
				t = statusTimer
			}

			klog.V(4).Infof("Received status update for node %s", n.n.Name)
			// Performing a status update so stop/reset the status update timer in this
			// branch otherwise there could be an unnecessary status update.
			if !t.Stop() {
				<-t.C
			}

			n.n.Status = updated.Status
			if err := n.updateStatus(ctx, false); err != nil {
				klog.Error(err, " - Error handling node status update")
			}
			t.Reset(timerResetDuration)
		case <-statusTimer.C:
			if err := n.updateStatus(ctx, false); err != nil {
				klog.Error(err, " - Error handling node status update")
			}
			statusTimer.Reset(n.statusInterval)
		case <-pingTimer.C:
			if err := n.handlePing(ctx); err != nil {
				klog.Error(err, " - Error while handling node ping")
			} else {
				klog.V(4).Info("Successful node ping")
			}
			pingTimer.Reset(n.pingInterval)
		}
	}
}

func (n *NodeController) handlePing(ctx context.Context) (retErr error) {
	if err := n.p.Ping(ctx); err != nil {
		return pkgerrors.Wrap(err, "error while pinging the node provider")
	}

	if n.disableLease {
		return n.updateStatus(ctx, false)
	}

	return n.updateLease(ctx)
}

func (n *NodeController) updateLease(ctx context.Context) error {
	l, err := updateNodeLease(ctx, n.leases, newLease(n.lease))
	if err != nil {
		return err
	}

	n.lease = l
	return nil
}

func (n *NodeController) UpdateNodeFromOutside(skipErrorCb bool, no *corev1.Node) error {
	n.n = no
	return n.updateStatus(context.Background(), skipErrorCb)
}

func (n *NodeController) updateStatus(ctx context.Context, skipErrorCb bool) error {
	n.statusUpdateMutex.Lock()
	defer n.statusUpdateMutex.Unlock()

	updateNodeStatusHeartbeat(n.n)

	node, err := updateNodeStatus(ctx, n.nodes, n.n)
	if err != nil {
		if skipErrorCb || n.nodeStatusUpdateErrorHandler == nil {
			return err
		}
		if err := n.nodeStatusUpdateErrorHandler(ctx, err); err != nil {
			return err
		}

		node, err = updateNodeStatus(ctx, n.nodes, n.n)
		if err != nil {
			return err
		}
	}

	n.n = node
	return nil
}

func ensureLease(ctx context.Context, leases coordinationv1.LeaseInterface, lease *coord.Lease) (*coord.Lease, error) {
	l, err := leases.Create(context.TODO(), lease, metav1.CreateOptions{})
	if err != nil {
		switch {
		case errors.IsNotFound(err):
			klog.Error(err, " - Node lease not supported")
			return nil, err
		case errors.IsAlreadyExists(err):
			if err := leases.Delete(context.TODO(), lease.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
				klog.Error(err, " - could not delete old node lease")
				return nil, pkgerrors.Wrap(err, "old lease exists but could not delete it")
			}
			l, err = leases.Create(context.TODO(), lease, metav1.CreateOptions{})
		}
	}

	return l, err
}

// updateNodeLease updates the node lease.
//
// If this function returns an errors.IsNotFound(err) error, this likely means
// that node leases are not supported, if this is the case, call updateNodeStatus
// instead.
//
// NOTE: this code has been modified to fix a bug already addressed in the upstream
// virtual kubelet repository. It can be dropped when upgrading the kubelet code.
func updateNodeLease(ctx context.Context, leases coordinationv1.LeaseInterface, lease *coord.Lease) (*coord.Lease, error) {
	var l *coord.Lease
	var err error
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		l, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				klog.V(4).Infof("lease %s/%s not found", lease.Namespace, lease.Name)
				l, err = ensureLease(ctx, leases, lease)
			}
			if errors.IsConflict(err) {
				klog.V(4).Infof("conflict, get lease %s/%s", lease.Namespace, lease.Name)
				var newErr error
				lease, newErr = leases.Get(ctx, lease.GetName(), metav1.GetOptions{})
				if newErr != nil {
					klog.Error(newErr)
					return newErr
				}
				return err
			}
			if err != nil {
				return err
			}
			klog.V(4).Infof("created new lease %s%s", lease.Namespace, lease.Name)
		} else {
			klog.V(4).Infof("updated lease %s/%s", lease.Namespace, lease.Name)
		}

		return nil
	})
	return l, err
}

// just so we don't have to allocate this on every get request.
var emptyGetOptions = metav1.GetOptions{}

// patchNodeStatus patches node status.
// Copied from github.com/kubernetes/kubernetes/pkg/util/node.
func patchNodeStatus(nodes v1.NodeInterface, nodeName types.NodeName, oldNode *corev1.Node, newNode *corev1.Node) (*corev1.Node, []byte, error) {
	patchBytes, err := preparePatchBytesforNodeStatus(nodeName, oldNode, newNode)
	if err != nil {
		return nil, nil, err
	}

	updatedNode, err := nodes.Patch(context.TODO(), string(nodeName), types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to patch status %q for node %q: %v", patchBytes, nodeName, err)
	}
	return updatedNode, patchBytes, nil
}

func preparePatchBytesforNodeStatus(nodeName types.NodeName, oldNode *corev1.Node, newNode *corev1.Node) ([]byte, error) {
	oldData, err := json.Marshal(oldNode)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal oldData for node %q: %v", nodeName, err)
	}

	// Reset spec to make sure only patch for Status or ObjectMeta is generated.
	// Note that we don't reset ObjectMeta here, because:
	// 1. This aligns with Nodes().UpdateStatus().
	// 2. Some component does use this to update node annotations.
	newNode.Spec = oldNode.Spec
	newData, err := json.Marshal(newNode)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal newData for node %q: %v", nodeName, err)
	}

	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch for node %q: %v", nodeName, err)
	}
	return patchBytes, nil
}

// updateNodeStatus triggers an update to the node status in Kubernetes.
// It first fetches the current node details and then sets the status according
// to the passed in node object.
//
// If you use this function, it is up to you to synchronize this with other operations.
// This reduces the time to second-level precision.
func updateNodeStatus(ctx context.Context, nodes v1.NodeInterface, n *corev1.Node) (_ *corev1.Node, retErr error) {
	var node *corev1.Node

	oldNode, err := nodes.Get(context.TODO(), n.Name, emptyGetOptions)
	if err != nil {
		return nil, err
	}

	klog.V(4).Infof("got node %s from api server", n.Name)
	node = oldNode.DeepCopy()
	node.ResourceVersion = ""
	node.Status = n.Status

	// Patch the node status to merge other changes on the node.
	updated, _, err := patchNodeStatus(nodes, types.NodeName(n.Name), oldNode, node)
	if err != nil {
		return nil, err
	}

	klog.V(4).Infof("updated node %s status in api server", n.Name)
	return updated, nil
}

func newLease(base *coord.Lease) *coord.Lease {
	var lease *coord.Lease
	if base == nil {
		lease = &coord.Lease{}
	} else {
		lease = base.DeepCopy()
	}

	lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
	return lease
}

func setLeaseAttrs(l *coord.Lease, n *corev1.Node, dur time.Duration) {
	if l.Name == "" {
		l.Name = n.Name
	}
	if l.Spec.HolderIdentity == nil {
		l.Spec.HolderIdentity = &n.Name
	}

	if l.Spec.LeaseDurationSeconds == nil {
		d := int32(dur.Seconds()) * 5
		l.Spec.LeaseDurationSeconds = &d
	}
}

func updateNodeStatusHeartbeat(n *corev1.Node) {
	now := metav1.NewTime(time.Now())
	for i := range n.Status.Conditions {
		n.Status.Conditions[i].LastHeartbeatTime = now
	}
}

// NaiveNodeProvider is a basic node provider that only uses the passed in context
// on `Ping` to determine if the node is healthy.
type NaiveNodeProvider struct{}

// Ping just implements the NodeProvider interface.
// It returns the error from the passed in context only.
func (NaiveNodeProvider) Ping(ctx context.Context) error {
	return ctx.Err()
}

// NotifyNodeStatus implements the NodeProvider interface.
//
// This NaiveNodeProvider does not support updating node status and so this
// function is a no-op.
func (NaiveNodeProvider) NotifyNodeStatus(ctx context.Context, f func(*corev1.Node)) {
}
