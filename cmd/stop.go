package cmd

import (
	"context"
	"fmt"
	"time"

	k8sinternal "github.com/1mr0-tech/tether/internal/k8s"
	"github.com/1mr0-tech/tether/internal/token"
	"github.com/spf13/cobra"
)

var stopSessionToken string

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop intercepting and restore the original service",
	RunE:  runStop,
}

func init() {
	stopCmd.Flags().StringVar(&stopSessionToken, "session", "", "session token (required)")
	_ = stopCmd.MarkFlagRequired("session")
}

func runStop(cmd *cobra.Command, args []string) error {
	tok, err := token.Decode(stopSessionToken)
	if err != nil {
		return err
	}

	state, err := k8sinternal.LoadState(tok.ID)
	if err != nil {
		return err
	}

	client, err := buildK8sClient()
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}

	cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Restore service first (most critical — gets traffic back to original pods).
	fmt.Printf("Restoring %s/%s...\n", state.Namespace, state.ServiceName)
	if err := k8sinternal.RestoreService(cleanCtx, client, state); err != nil {
		return fmt.Errorf("restore service: %w", err)
	}

	// Signal the agent to close its listener for this session.
	if err := sendOpsCommand(tok.Relay, "close", tok.ID, 0); err != nil {
		fmt.Printf("warning: could not signal agent to close session: %v\n", err)
	}

	k8sinternal.DeleteState(tok.ID)

	fmt.Printf("Done. %s/%s is back to its original pods.\n", state.Namespace, state.ServiceName)
	return nil
}
