package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	k8sinternal "github.com/imranroshan/tether/internal/k8s"
	"github.com/imranroshan/tether/internal/token"
	"github.com/spf13/cobra"
)

var (
	startNamespace string
	startRelay     string
)

var startCmd = &cobra.Command{
	Use:   "start <deployment>",
	Short: "Start intercepting a deployment's traffic",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

func init() {
	startCmd.Flags().StringVarP(&startNamespace, "namespace", "n", "default", "namespace of the deployment")
	startCmd.Flags().StringVar(&startRelay, "relay", "", "relay server address (required)")
	_ = startCmd.MarkFlagRequired("relay")
}

func runStart(cmd *cobra.Command, args []string) error {
	deploymentName := args[0]

	client, err := buildK8sClient()
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}

	// Find the Service that fronts this Deployment.
	svc, targetPort, err := k8sinternal.FindServiceForDeployment(cmd.Context(), client, startNamespace, deploymentName)
	if err != nil {
		return err
	}
	fmt.Printf("Found service %s (targetPort: %d)\n", svc.Name, targetPort)

	// Generate session ID.
	sessionID, err := newSessionID()
	if err != nil {
		return err
	}

	// Signal the always-on agent (via relay) to open a listener on targetPort.
	fmt.Printf("Opening relay session...\n")
	if err := sendOpsCommand(startRelay, "open", sessionID, targetPort); err != nil {
		return fmt.Errorf("signal agent: %w", err)
	}

	// Get the agent pod's cluster IP to create Endpoints pointing at it.
	agentIP, err := k8sinternal.GetAgentPodIP(cmd.Context(), client)
	if err != nil {
		_ = sendOpsCommand(startRelay, "close", sessionID, 0)
		return err
	}

	// Route the Service to the agent pod (cross-namespace via Endpoints).
	if err := k8sinternal.SwitchToAgent(cmd.Context(), client, startNamespace, svc.Name, agentIP, targetPort); err != nil {
		_ = sendOpsCommand(startRelay, "close", sessionID, 0)
		return fmt.Errorf("switch service: %w", err)
	}

	// Persist state for crash recovery via `tether stop`.
	state := &k8sinternal.SessionState{
		SessionID:        sessionID,
		Relay:            startRelay,
		Namespace:        startNamespace,
		ServiceName:      svc.Name,
		OriginalSelector: svc.Spec.Selector,
		TargetPort:       targetPort,
	}
	if err := k8sinternal.SaveState(state); err != nil {
		fmt.Printf("warning: could not save session state: %v\n", err)
	}

	// Build the opaque token — encodes relay addr + session ID.
	tok := token.Encode(token.Session{ID: sessionID, Relay: startRelay})

	fmt.Printf("\nIntercepting %s/%s → developer laptop\n\n", startNamespace, deploymentName)
	fmt.Println("Share this with the developer:")
	fmt.Printf("\n  tether connect --session %s --port <local-port>\n\n", tok)
	fmt.Printf("To stop:  tether stop --session %s\n", tok)
	return nil
}

// sendOpsCommand dials the relay, sends a one-shot control command, and reads the ack.
func sendOpsCommand(relayAddr, action, session string, port int) error {
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
