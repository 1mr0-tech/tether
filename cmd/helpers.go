package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func buildK8sClient() (kubernetes.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, overrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// startPortForward starts kubectl port-forward for the tether-relay service in
// the background and waits until the port is accepting connections.
// Returns a cancel function that kills the subprocess. Safe to call even if
// port-forward fails (returns nil cancel + error).
func startPortForward(localPort string) (cancel func(), err error) {
	// #nosec G204 -- localPort is an integer parsed from the relay address
	cmd := exec.Command("kubectl", "port-forward",
		"svc/tether-relay",
		localPort+":8080",
		"-n", "tether",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start port-forward: %w", err)
	}

	// Poll until the forwarded port is listening (up to 5s).
	addr := "localhost:" + localPort
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return func() { _ = cmd.Process.Kill() }, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("port-forward to relay timed out — is the relay pod running?")
}

// portFromAddr extracts the port string from a "host:port" address.
func portFromAddr(addr string) string {
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return addr
}
