package cmd

import (
	"github.com/imranroshan/tether/internal/relay"
	"github.com/spf13/cobra"
)

var serverAddr string

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the relay server",
	RunE: func(cmd *cobra.Command, args []string) error {
		s := relay.NewServer(serverAddr)
		return s.ListenAndServe()
	},
}

func init() {
	serverCmd.Flags().StringVar(&serverAddr, "addr", ":8080", "address to listen on")
}
