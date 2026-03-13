package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const agentNamespace = "tether"

// GetAgentPodIP returns the IP of the running agent pod in the tether namespace.
func GetAgentPodIP(ctx context.Context, client kubernetes.Interface) (string, error) {
	pods, err := client.CoreV1().Pods(agentNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=tether-agent",
	})
	if err != nil {
		return "", fmt.Errorf("list agent pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			return pod.Status.PodIP, nil
		}
	}
	return "", fmt.Errorf("no running agent pod found — run 'tether install' first")
}

// SwitchToAgent intercepts a service by removing its selector and pointing
// its Endpoints directly at the agent pod IP. Works cross-namespace.
func SwitchToAgent(ctx context.Context, client kubernetes.Interface, namespace, serviceName, agentIP string, targetPort int) error {
	// Remove the selector so k8s stops managing Endpoints automatically.
	selectorPatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"selector": nil,
		},
	}
	data, _ := json.Marshal(selectorPatch)
	if _, err := client.CoreV1().Services(namespace).Patch(
		ctx, serviceName, types.MergePatchType, data, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("remove service selector: %w", err)
	}

	// Create Endpoints pointing to the agent pod.
	// Validate targetPort is in a valid uint16 range to prevent integer overflow (CWE-190).
	if targetPort < 1 || targetPort > 65535 {
		return fmt.Errorf("invalid targetPort %d: must be 1-65535", targetPort)
	}
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{{IP: agentIP}},
				Ports:     []corev1.EndpointPort{{Port: int32(targetPort)}}, // #nosec G115 -- validated above
			},
		},
	}
	// Update existing Endpoints (k8s creates one automatically alongside the Service).
	_, err := client.CoreV1().Endpoints(namespace).Update(ctx, ep, metav1.UpdateOptions{})
	if err != nil {
		// Fallback: create if update fails.
		_, err = client.CoreV1().Endpoints(namespace).Create(ctx, ep, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("create endpoints: %w", err)
	}

	// Delete any EndpointSlices managed by the selector-based controller.
	// In k8s 1.21+ the endpointslice-controller does NOT delete its slices when
	// the Service selector is removed — leaving stale ready endpoints that cause
	// kube-proxy to load-balance between the agent and the original pods.
	deleteOldEndpointSlices(ctx, client, namespace, serviceName)
	return nil
}

// deleteOldEndpointSlices removes EndpointSlices that are still managed by the
// endpointslice-controller (i.e. selector-driven) for the given service.
// The mirroring-controller's slice (derived from our manual Endpoints) is kept.
func deleteOldEndpointSlices(ctx context.Context, client kubernetes.Interface, namespace, serviceName string) {
	slices, err := client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=" + serviceName,
	})
	if err != nil {
		log.Printf("warn: list EndpointSlices for %s: %v", serviceName, err)
		return
	}
	for _, s := range slices.Items {
		if s.Labels["endpointslice.kubernetes.io/managed-by"] == "endpointslice-controller.k8s.io" {
			if err := client.DiscoveryV1().EndpointSlices(namespace).Delete(ctx, s.Name, metav1.DeleteOptions{}); err != nil {
				log.Printf("warn: delete EndpointSlice %s: %v", s.Name, err)
			} else {
				log.Printf("deleted stale EndpointSlice %s", s.Name)
			}
		}
	}
}

// RestoreService re-adds the original selector and removes the manual Endpoints
// so k8s resumes managing them automatically.
func RestoreService(ctx context.Context, client kubernetes.Interface, state *SessionState) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"selector": state.OriginalSelector,
		},
	}
	data, _ := json.Marshal(patch)
	if _, err := client.CoreV1().Services(state.Namespace).Patch(
		ctx, state.ServiceName, types.MergePatchType, data, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("restore service selector: %w", err)
	}

	// Delete the manual Endpoints — k8s will recreate them from the selector.
	_ = client.CoreV1().Endpoints(state.Namespace).Delete(ctx, state.ServiceName, metav1.DeleteOptions{})
	return nil
}
