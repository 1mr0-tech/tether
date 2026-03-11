package cmd

import (
	"fmt"

	"github.com/imranroshan/tether/internal/client"
	"github.com/imranroshan/tether/internal/token"
	"github.com/spf13/cobra"
)

var (
	connectSessionToken string
	connectLocalPort    int
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Forward intercepted traffic to a local port",
	RunE:  runConnect,
}

func init() {
	connectCmd.Flags().StringVar(&connectSessionToken, "session", "", "session token (required)")
	connectCmd.Flags().IntVarP(&connectLocalPort, "port", "p", 0, "local port to forward traffic to (required)")
	_ = connectCmd.MarkFlagRequired("session")
	_ = connectCmd.MarkFlagRequired("port")
}

func runConnect(cmd *cobra.Command, args []string) error {
	tok, err := token.Decode(connectSessionToken)
	if err != nil {
		return err
	}

	fmt.Printf("Connected. Forwarding traffic to localhost:%d\n", connectLocalPort)
	fmt.Println("Press Ctrl+C to disconnect.")

	return client.Run(cmd.Context(), client.Config{
		RelayAddr: tok.Relay,
		SessionID: tok.ID,
		LocalPort: connectLocalPort,
	})
}
