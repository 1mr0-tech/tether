package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// sessionIDRe validates that session IDs are strictly lowercase hex (32 chars).
// This prevents path traversal in state file operations (CWE-22 / gosec G304).
var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

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

// ListNamespaces returns user-facing namespaces (excludes system and tether namespaces).
func ListNamespaces(ctx context.Context, client kubernetes.Interface) ([]string, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	skip := map[string]bool{
		"kube-system":      true,
		"kube-public":      true,
		"kube-node-lease":  true,
		agentNamespace:     true,
	}
	var names []string
	for _, ns := range list.Items {
		if !skip[ns.Name] {
			names = append(names, ns.Name)
		}
	}
	return names, nil
}

// ListDeployments returns all deployment names in a namespace.
func ListDeployments(ctx context.Context, client kubernetes.Interface, namespace string) ([]string, error) {
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in %s: %w", namespace, err)
	}
	names := make([]string, len(list.Items))
	for i, d := range list.Items {
		names[i] = d.Name
	}
	return names, nil
}

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
	if !sessionIDRe.MatchString(state.SessionID) {
		return fmt.Errorf("invalid session ID format")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".tether", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// filepath.Join is safe here: dir is derived from home, sessionID is validated above.
	return os.WriteFile(filepath.Join(dir, state.SessionID+".json"), data, 0600) //nolint:gosec
}

// LoadState reads a persisted session state by session ID.
func LoadState(sessionID string) (*SessionState, error) {
	if !sessionIDRe.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid session ID format")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	path := filepath.Join(home, ".tether", "sessions", sessionID+".json")
	// path is safe: home from os.UserHomeDir(), sessionID validated by regexp above.
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("no saved state for session %s (already restored?)", sessionID)
	}
	var state SessionState
	return &state, json.Unmarshal(data, &state)
}

// DeleteState removes the state file after a successful stop.
func DeleteState(sessionID string) {
	if !sessionIDRe.MatchString(sessionID) {
		return
	}
	home, _ := os.UserHomeDir()
	_ = os.Remove(filepath.Join(home, ".tether", "sessions", sessionID+".json"))
}
