package util

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/api/v1/pod"

	"github.com/liqotech/liqo/pkg/virtualKubelet"
)

// IsPodUp waits for a specific namespace/podName to be ready. It returns true if the pod within the timeout, false otherwise.
func IsPodUp(ctx context.Context, client kubernetes.Interface, namespace, podName string, isHomePod bool) bool {
	var podToCheck *corev1.Pod
	var err error
	var labelSelector = map[string]string{
		virtualKubelet.ReflectedpodKey: podName,
	}
	if isHomePod {
		podToCheck, err = client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false
		}
	} else {
		pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(labelSelector).String(),
		})
		if err != nil || len(pods.Items) == 0 {
			return false
		}
		podToCheck = &pods.Items[0]
	}
	state := pod.IsPodReady(podToCheck)
	return state
}

// ArePodsUp check if all the pods of a specific namespace are ready. It returns a list of ready pods, a list of unready
// pods and occurred errors.
func ArePodsUp(ctx context.Context, clientset kubernetes.Interface, namespace string) (ready, notReady []string, retErr error) {
	pods, retErr := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if retErr != nil {
		klog.Error(retErr)
		return nil, nil, retErr
	}
	for index := range pods.Items {
		if !pod.IsPodReady(&pods.Items[index]) {
			notReady = append(notReady, pods.Items[index].Name)
		}
		ready = append(ready, pods.Items[index].Name)
	}
	return ready, notReady, nil
}
