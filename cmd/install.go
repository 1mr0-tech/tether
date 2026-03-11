package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	k8sinternal "github.com/1mr0-tech/tether/internal/k8s"
	"github.com/spf13/cobra"
)

var (
	installRelayAddr  string
	installAgentRelay string
	installImage      string
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Deploy the tether agent into the cluster (run once per cluster)",
	RunE:  runInstall,
}

func init() {
	installCmd.Flags().StringVar(&installRelayAddr, "relay", "", "relay address for ops CLI to connect to")
	installCmd.Flags().StringVar(&installAgentRelay, "agent-relay", "", "relay address the in-cluster agent connects to (defaults to --relay)")
	installCmd.Flags().StringVar(&installImage, "image", "", "agent container image")
}

func runInstall(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("=== tether installer ===")
	fmt.Println()

	// --- Relay address ---
	if installRelayAddr == "" {
		fmt.Println("Where is the relay server running?")
		fmt.Println("  1) On this machine (local k3d / minikube)")
		fmt.Println("  2) On an external server (shared dev/staging cluster)")
		fmt.Print("Choice [1/2]: ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		switch choice {
		case "1":
			fmt.Print("Relay port on this machine [8085]: ")
			port, _ := reader.ReadString('\n')
			port = strings.TrimSpace(port)
			if port == "" {
				port = "8085"
			}
			installRelayAddr = "localhost:" + port
			// Agent pods inside Docker/k3d reach the host via host.docker.internal
			if installAgentRelay == "" {
				installAgentRelay = "host.docker.internal:" + port
			}
			fmt.Printf("\n  Ops CLI relay:  %s\n", installRelayAddr)
			fmt.Printf("  Agent relay:    %s\n\n", installAgentRelay)

		case "2":
			fmt.Print("Relay server address (host:port): ")
			addr, _ := reader.ReadString('\n')
			addr = strings.TrimSpace(addr)
			if addr == "" {
				return fmt.Errorf("relay address is required")
			}
			installRelayAddr = addr
			if installAgentRelay == "" {
				installAgentRelay = addr
			}
			fmt.Printf("\n  Relay: %s\n\n", installRelayAddr)

		default:
			return fmt.Errorf("invalid choice")
		}
	} else if installAgentRelay == "" {
		installAgentRelay = installRelayAddr
	}

	// --- Image ---
	if installImage == "" {
		fmt.Print("Agent container image [tether:dev]: ")
		img, _ := reader.ReadString('\n')
		img = strings.TrimSpace(img)
		if img == "" {
			img = "tether:dev"
		}
		installImage = img
	}

	// --- Confirm ---
	fmt.Println("Installing with:")
	fmt.Printf("  Image:        %s\n", installImage)
	fmt.Printf("  Agent relay:  %s\n", installAgentRelay)
	fmt.Print("\nProceed? [Y/n]: ")
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm == "n" || confirm == "no" {
		fmt.Println("Aborted.")
		return nil
	}

	client, err := buildK8sClient()
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}

	fmt.Println("\nDeploying agent...")
	if err := k8sinternal.InstallAgent(cmd.Context(), client, installImage, installAgentRelay); err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	fmt.Println("\nAgent is ready.")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  Start the relay:   tether server --addr :%s\n", port(installRelayAddr))
	fmt.Printf("  Intercept a service: tether start <deployment> --relay %s\n", installRelayAddr)
	return nil
}

func port(addr string) string {
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return addr
}
