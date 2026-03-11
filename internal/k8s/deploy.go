package k8s

import (
	"context"
	"fmt"
	"log"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// InstallAgent deploys the always-on agent Deployment into the tether namespace.
func InstallAgent(ctx context.Context, client kubernetes.Interface, image, relayAddr string) error {
	// Ensure namespace exists.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		// Ignore already-exists error.
		if !isAlreadyExists(err) {
			return fmt.Errorf("create namespace: %w", err)
		}
	}

	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tether-agent",
			Namespace: agentNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "tether-agent"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "tether-agent"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: image,
							Args:  []string{"agent"},
							Env: []corev1.EnvVar{
								{Name: "RELAY_ADDR", Value: relayAddr},
							},
						},
					},
				},
			},
		},
	}

	_, err := client.AppsV1().Deployments(agentNamespace).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		if isAlreadyExists(err) {
			log.Printf("agent deployment already exists, skipping")
			return nil
		}
		return fmt.Errorf("create agent deployment: %w", err)
	}
	log.Printf("created agent deployment in namespace %s", agentNamespace)

	return waitForAgentReady(ctx, client)
}

func waitForAgentReady(ctx context.Context, client kubernetes.Interface) error {
	pollCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	log.Printf("waiting for agent pod to be ready...")
	return wait.PollUntilContextTimeout(pollCtx, 2*time.Second, 120*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := client.CoreV1().Pods(agentNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=tether-agent",
		})
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

func isAlreadyExists(err error) bool {
	return err != nil && contains(err.Error(), "already exists")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
