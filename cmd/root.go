package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	kubeconfig  string
	kubecontext string
)

var rootCmd = &cobra.Command{
	Use:   "tether",
	Short: "Route k8s service traffic to your local machine",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	rootCmd.PersistentFlags().StringVar(&kubecontext, "context", "", "kubernetes context to use")

	// Ops commands
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)

	// Developer command
	rootCmd.AddCommand(connectCmd)

	// Infrastructure
	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(agentCmd) // hidden
}
