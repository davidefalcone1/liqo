---
title: Computing
weight: 3
---

### Overview

The Offloading of the local Pods in the remote cluster consists in a Pod's creation followed by a new remote scheduling
phase. Once the remote Pod is scheduled, its status changes (the Kubelet of the node running the actual Pod reconciles 
it), then the local representation of the remote Pod has to be updated, to ensure complete visibility on the running Pod
in the remote cluster.

### Remote Pod resiliency

In Kubernetes, Pods are commonly created starting from a Deployment or a Job; in the case of deployments, the
controller-manager creates a new ReplicaSet that leads to a Pod creation (again, through the controller-manager). The
ReplicaSet-controller in the controller-manager is in charge of reconciling the desired status with the current one, 
i.e., whenever the number of existing Pods owned by a ReplicaSet changes, the controller-manager creates or delete some 
of them to reach the desired number of existing replicas.

In the Liqo context, every time the local Pod is deleted (intentionally or by eviction), the remote one has to be 
deleted as well: the local cluster is the owner of the remote Pod, therefore whenever an offloaded Pod deletion happens,
the remote cluster status has to be aligned, leading to the deletion of the remote Pod.

Contrariwise, whenever a remote Pod is deleted (intentionally or by eviction), the local one has not to be deleted: the
cluster hosting the offloaded Pod has not the right to trigger a re-scheduling in the local cluster. Additionally, if, 
for any reason, the local cluster cannot communicate with the remote one, and the remote Pod is deleted, at that point, 
there will not be any existing Pod.
For these reasons, the Virtual Kubelet creates remote ReplicaSets instead of Pods: once a ReplicaSet is created in the 
remote cluster, the remote ReplicaSet-controller reconciles its status, leading to one existing Pod at any time 
(the desired amount of replicas for those ReplicaSet is always one).

### Computing resources offloading and reconciliation

The scheme below describes the offloading workflow. The local Pod is referred to as shadow Pod (because it is a mere
local representation of the remote Pod).

![](/images/offloading/computing-offloading-overview.svg)

1. A user creates a deployment in the local cluster
2. The controller-manager detects the deployment creation, then
    1. Creates the corresponding ReplicaSet
    2. Detects the ReplicaSet creation, and 
    3. Creates the specified amount of Pod replicas
3. The scheduler detects the Pod creation, and
4. binds some Pods to the virtual node
5. The Virtual Kubelet detects that a Pod has been scheduled on the virtual node managed by it
7. The Virtual Kubelet creates a remote ReplicaSet having the local Pod as `PodTemplate` field and 1 replica
8. The remote controller-manager detects the ReplicaSet and 
9. Creates one Pod starting from the `PodTemplate` field
10. The Virtual Kubelet detects the creation of the remote offloaded Pod
11. the Virtual Kubelet keeps the local Pod status updated with the local one.
