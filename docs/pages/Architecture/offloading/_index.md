---
title: Offloading
weight: 4
---

## Overview

Once the peering process has been completed, a new virtual node appears in our cluster: this node aims at masquerading a 
remote cluster. 
The component in charge of managing a node in Kubernetes is the 
[Kubelet](https://kubernetes.io/docs/reference/command-line-tools-reference/kubelet/), a process running in the
hosting machine that manages its API representation in Kubernetes, and the lifecycle of the pods scheduled on it. Since
the remote cluster is represented by a local node, the offloading of some pods to the remote cluster is fully compliant
with the Kubernetes control plane: whenever a pod is scheduled to the virtual node, the pod is then offloaded to the 
remote cluster. When the remote cluster receives a new pod, a new remote scheduling phase happens, in which the remote 
scheduler elects one node as host for the received pod, and the kubelet managing that pods takes charge of the
containers' execution.

### Virtual Kubelet

Since the node cannot be managed by a real process on the physical machine (actually there is no real node representing 
the remote cluster), the kubelet that manages the virtual node is containerized and executed as a pod of the Liqo 
control plane. We implemented our version of the [Virtual kubelet](https://github.com/virtual-kubelet/virtual-kubelet)
project for the virtual node management. 

Generally speaking, a real Kubelet is in charge of accomplishing two duties:
* handling the node resource and reconciling its status
* taking the received pods, starting the containers, and reconciling their status in the pod resource.

Similarly, the virtual kubelet is in charge of:
* creating the virtual node resource and reconciling its status, as described in the 
[node-management](features/node-management) section
* offloading the local pod scheduled on the virtual node to the remote cluster, as described in the 
[computing](features/computing) section.

Also, our implementation provides a feature we called "reflection", described [here](features/api-reflection)

### namespace mapping

To make the pods in a certain namespace suitable to be offloaded in the remote cluster, the virtual Kubelet has to face
with the problem of the offloading namespace, i.e., in which namespace of the remote cluster to create the pods.
Further details can be found in the dedicated [section](features/namespace-management)

### Scheduling behavior

The virtual node is created with a specific taint. To make a pod available to be scheduled on that node, that taint must
be tolerated. The toleration is added by a `MutatingWebhook` watching all the pods being created in all the namespaces 
labeled with the label `liqo.io/enabled="true"`.

By default, the Kubernetes scheduler selects the eligible node with the highest score (scores are computed on
several parameters, among which the available resources).
Given that the virtual node summarizes all the resources shared by a given foreign cluster (no matter how many remote 
physical nodes are involved), is very likely that the above node will be perceived as fatter than any physical node 
available locally. Hence, very likely, new pods will be scheduled on that node.
