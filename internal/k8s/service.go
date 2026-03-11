package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// SessionState is persisted to disk on ops start for crash recovery.
type SessionState struct {
	SessionID        string            `json:"sessionID"`
	Relay            string            `json:"relay"`
	Namespace        string            `json:"namespace"`
	ServiceName      string            `json:"serviceName"`
	OriginalSelector map[string]string `json:"originalSelector"`
	TargetPort       int               `json:"targetPort"`
}

// FindServiceForDeployment finds the Service whose selector matches the Deployment's pod labels.
func FindServiceForDeployment(ctx context.Context, client kubernetes.Interface, namespace, deploymentName string) (*corev1.Service, int, error) {
	dep, err := client.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("deployment %s/%s not found: %w", namespace, deploymentName, err)
	}

	podLabels := dep.Spec.Selector.MatchLabels

	svcs, err := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("list services in %s: %w", namespace, err)
	}

	for i := range svcs.Items {
		svc := &svcs.Items[i]
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		if selectorMatches(svc.Spec.Selector, podLabels) {
			targetPort, err := resolveTargetPort(svc)
			if err != nil {
				return nil, 0, err
			}
			return svc, targetPort, nil
		}
	}

	return nil, 0, fmt.Errorf("no service found matching deployment %s in namespace %s", deploymentName, namespace)
}

// selectorMatches returns true if all keys in svcSelector match podLabels.
func selectorMatches(svcSelector, podLabels map[string]string) bool {
	for k, v := range svcSelector {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}

func resolveTargetPort(svc *corev1.Service) (int, error) {
	if len(svc.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service %s has no ports", svc.Name)
	}
	tp := svc.Spec.Ports[0].TargetPort
	switch tp.Type {
	case intstr.Int:
		return int(tp.IntVal), nil
	case intstr.String:
		return 0, fmt.Errorf("named targetPort %q not yet supported; use a numeric targetPort", tp.StrVal)
	default:
		return int(svc.Spec.Ports[0].Port), nil
	}
}

// SaveState persists session state for crash recovery.
func SaveState(state *SessionState) error {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".tether", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, state.SessionID+".json"), data, 0600)
}

// LoadState reads a persisted session state by session ID.
func LoadState(sessionID string) (*SessionState, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".tether", "sessions", sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no saved state for session %s (already restored?)", sessionID)
	}
	var state SessionState
	return &state, json.Unmarshal(data, &state)
}

// DeleteState removes the state file after a successful stop.
func DeleteState(sessionID string) {
	home, _ := os.UserHomeDir()
	_ = os.Remove(filepath.Join(home, ".tether", "sessions", sessionID+".json"))
}
