package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	k8sinternal "github.com/1mr0-tech/tether/internal/k8s"
	"github.com/1mr0-tech/tether/internal/token"
	"github.com/spf13/cobra"
)

var startNamespace string

var startCmd = &cobra.Command{
	Use:   "start [deployment]",
	Short: "Intercept a deployment's traffic (interactive if no args given)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStart,
}

func init() {
	startCmd.Flags().StringVarP(&startNamespace, "namespace", "n", "", "namespace of the deployment")
}

func runStart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	k8sClient, err := buildK8sClient()
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}

	// ── Read relay config from cluster ────────────────────────────────────────
	cfg, err := k8sinternal.ReadConfig(ctx, k8sClient)
	if err != nil {
		return err
	}

	// For k3d / macOS: relay NodePort is not directly reachable — use port-forward.
	if cfg.UsePortForward {
		port := portFromAddr(cfg.RelayExternal)
		fmt.Printf("Starting kubectl port-forward to relay (localhost:%s)...\n", port)
		cancel, err := startPortForward(port)
		if err != nil {
			return fmt.Errorf("relay port-forward: %w\n  Ensure the relay pod is running: kubectl get pods -n tether", err)
		}
		defer cancel()
	}

	reader := bufio.NewReader(os.Stdin)

	// ── Namespace: use flag or prompt ─────────────────────────────────────────
	namespace := startNamespace
	if namespace == "" {
		namespaces, err := k8sinternal.ListNamespaces(ctx, k8sClient)
		if err != nil {
			return err
		}
		if len(namespaces) == 0 {
			return fmt.Errorf("no namespaces found (excluding system namespaces)")
		}

		fmt.Println()
		fmt.Println("Available namespaces:")
		for i, ns := range namespaces {
			fmt.Printf("  %d) %s\n", i+1, ns)
		}
		fmt.Println()

		namespace, err = pickFromList(reader, "Select namespace", namespaces)
		if err != nil {
			return err
		}
	}

	// ── Deployment: use arg or prompt ─────────────────────────────────────────
	deploymentName := ""
	if len(args) > 0 {
		deploymentName = args[0]
	} else {
		deployments, err := k8sinternal.ListDeployments(ctx, k8sClient, namespace)
		if err != nil {
			return err
		}
		if len(deployments) == 0 {
			return fmt.Errorf("no deployments found in namespace %q", namespace)
		}

		fmt.Printf("Deployments in %q:\n", namespace)
		for i, d := range deployments {
			fmt.Printf("  %d) %s\n", i+1, d)
		}
		fmt.Println()

		deploymentName, err = pickFromList(reader, "Select deployment", deployments)
		if err != nil {
			return err
		}
	}

	// ── Find the service ──────────────────────────────────────────────────────
	svc, targetPort, err := k8sinternal.FindServiceForDeployment(ctx, k8sClient, namespace, deploymentName)
	if err != nil {
		return err
	}
	fmt.Printf("\nFound service %s (targetPort: %d)\n", svc.Name, targetPort)

	// ── Generate session ID ───────────────────────────────────────────────────
	sessionID, err := newSessionID()
	if err != nil {
		return err
	}

	// ── Signal agent to open a port listener ──────────────────────────────────
	fmt.Println("Opening relay session...")
	if err := sendOpsCommand(cfg.RelayExternal, "open", sessionID, cfg.PSK, targetPort); err != nil {
		return fmt.Errorf("signal agent: %w", err)
	}

	// ── Route the service to the agent pod ────────────────────────────────────
	agentIP, err := k8sinternal.GetAgentPodIP(ctx, k8sClient)
	if err != nil {
		_ = sendOpsCommand(cfg.RelayExternal, "close", sessionID, cfg.PSK, 0)
		return err
	}

	if err := k8sinternal.SwitchToAgent(ctx, k8sClient, namespace, svc.Name, agentIP, targetPort); err != nil {
		_ = sendOpsCommand(cfg.RelayExternal, "close", sessionID, cfg.PSK, 0)
		return fmt.Errorf("switch service: %w", err)
	}

	// ── Persist state for crash recovery ──────────────────────────────────────
	state := &k8sinternal.SessionState{
		SessionID:        sessionID,
		Relay:            cfg.RelayExternal,
		Namespace:        namespace,
		ServiceName:      svc.Name,
		OriginalSelector: svc.Spec.Selector,
		TargetPort:       targetPort,
	}
	if err := k8sinternal.SaveState(state); err != nil {
		fmt.Printf("warning: could not save session state: %v\n", err)
	}

	// ── Build and print opaque session token ──────────────────────────────────
	tok := token.Encode(token.Session{
		ID:    sessionID,
		Relay: cfg.RelayExternal,
		PSK:   cfg.PSK,
	})

	fmt.Printf("\nIntercepting %s/%s → developer laptop\n", namespace, deploymentName)
	fmt.Println()
	fmt.Println("Share this command with the developer:")
	fmt.Println()
	fmt.Printf("  tether connect --session %s --port <local-port>\n", tok)
	fmt.Println()
	fmt.Printf("To stop:  tether stop --session %s\n", tok)
	return nil
}

// pickFromList shows a prompt and accepts either a number or the item name.
func pickFromList(r *bufio.Reader, prompt string, items []string) (string, error) {
	for {
		fmt.Printf("%s [1-%d]: ", prompt, len(items))
		input, _ := r.ReadString('\n')
		input = strings.TrimSpace(input)

		if n, err := strconv.Atoi(input); err == nil {
			if n >= 1 && n <= len(items) {
				return items[n-1], nil
			}
		}
		for _, item := range items {
			if item == input {
				return item, nil
			}
		}
		fmt.Printf("  Invalid selection — enter a number between 1 and %d, or the exact name.\n", len(items))
	}
}

// sendOpsCommand dials the relay, sends a one-shot control command with PSK, and reads the ack.
func sendOpsCommand(relayAddr, action, session, psk string, port int) error {
	conn, err := net.DialTimeout("tcp", relayAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial relay %s: %w", relayAddr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if err := json.NewEncoder(conn).Encode(map[string]interface{}{
		"role":    "ops",
		"action":  action,
		"session": session,
		"port":    port,
		"psk":     psk,
	}); err != nil {
		return fmt.Errorf("send command: %w", err)
	}

	var resp struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read relay response: %w", err)
	}
	if resp.Status != "ok" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

