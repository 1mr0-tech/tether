package cmd

import (
	"os"

	"github.com/1mr0-tech/tether/internal/relay"
	"github.com/spf13/cobra"
)

var serverAddr string

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the relay server (deployed automatically in-cluster by 'tether install')",
	RunE: func(cmd *cobra.Command, args []string) error {
		psk := os.Getenv("RELAY_PSK")
		return relay.NewServer(serverAddr, psk).ListenAndServe()
	},
}

func init() {
	serverCmd.Flags().StringVar(&serverAddr, "addr", ":8080", "address to listen on")
}
