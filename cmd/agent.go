package cmd

import (
	"fmt"
	"os"

	"github.com/1mr0-tech/tether/internal/agent"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:    "agent",
	Short:  "Run the in-cluster agent (reads RELAY_ADDR and RELAY_PSK env vars)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		relayAddr := os.Getenv("RELAY_ADDR")
		if relayAddr == "" {
			return fmt.Errorf("RELAY_ADDR env var must be set")
		}
		psk := os.Getenv("RELAY_PSK")
		if psk == "" {
			return fmt.Errorf("RELAY_PSK env var must be set")
		}
		return agent.Run(cmd.Context(), agent.Config{
			RelayAddr: relayAddr,
			PSK:       psk,
		})
	},
}
