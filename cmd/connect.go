package cmd

import (
	"fmt"
	"strings"

	"github.com/1mr0-tech/tether/internal/client"
	"github.com/1mr0-tech/tether/internal/token"
	"github.com/spf13/cobra"
)

var (
	connectSessionToken string
	connectLocalPort    int
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Forward intercepted cluster traffic to a local port",
	RunE:  runConnect,
}

func init() {
	connectCmd.Flags().StringVar(&connectSessionToken, "session", "", "session token from ops (required)")
	connectCmd.Flags().IntVarP(&connectLocalPort, "port", "p", 0, "local port to forward traffic to (required)")
	_ = connectCmd.MarkFlagRequired("session")
	_ = connectCmd.MarkFlagRequired("port")
}

func runConnect(cmd *cobra.Command, args []string) error {
	if connectLocalPort < 1 || connectLocalPort > 65535 {
		return fmt.Errorf("invalid port %d: must be 1-65535", connectLocalPort)
	}

	tok, err := token.Decode(connectSessionToken)
	if err != nil {
		return err
	}

	// For k3d / local dev: if relay is on localhost, auto-start kubectl port-forward.
	// This is transparent — the developer just runs 'tether connect' as normal.
	if strings.HasPrefix(tok.Relay, "localhost:") || strings.HasPrefix(tok.Relay, "127.0.0.1:") {
		port := portFromAddr(tok.Relay)
		cancel, err := startPortForward(port)
		if err != nil {
			fmt.Printf("Note: could not auto-start port-forward (%v)\n", err)
			fmt.Printf("If relay is unreachable, run in another terminal:\n")
			fmt.Printf("  kubectl port-forward svc/tether-relay %s:8080 -n tether\n\n", port)
		} else {
			defer cancel()
		}
	}

	fmt.Printf("Connected. Forwarding traffic to localhost:%d\n", connectLocalPort)
	fmt.Println("Press Ctrl+C to disconnect.")

	return client.Run(cmd.Context(), client.Config{
		RelayAddr: tok.Relay,
		SessionID: tok.ID,
		LocalPort: connectLocalPort,
		PSK:       tok.PSK,
	})
}
