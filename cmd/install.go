package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	k8sinternal "github.com/1mr0-tech/tether/internal/k8s"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
)

type clusterType string

const (
	clusterMinikube  clusterType = "minikube"
	clusterK3d       clusterType = "k3d"
	clusterK3sLocal  clusterType = "k3s-local"
	clusterUnknown   clusterType = "unknown"
)

var installForce bool

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Build the tether image and deploy it into the cluster (run once per cluster)",
	Long: `tether install detects your cluster type, builds the tether image locally,
imports it into the cluster without a registry, and deploys the relay and agent.

Run this command from inside the tether repository directory.`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVar(&installForce, "force", false, "reinstall even if tether is already installed (regenerates PSK)")
}

func runInstall(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// ── Locate repo root ──────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	// ── Detect cluster type ───────────────────────────────────────────────────
	contextName, serverURL, err := getKubeInfo()
	if err != nil {
		return fmt.Errorf("read kubeconfig: %w", err)
	}
	ct := detectClusterType(contextName, serverURL)

	printHeader("tether installer")
	fmt.Printf("  Context:  %s\n", contextName)
	fmt.Printf("  Cluster:  %s\n", string(ct))
	fmt.Println()

	// ── Check prerequisites ───────────────────────────────────────────────────
	printStep("Checking prerequisites")
	if err := checkPrerequisites(ct); err != nil {
		return err
	}

	// ── Connect to cluster ────────────────────────────────────────────────────
	k8sClient, err := buildK8sClient()
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}

	// ── Check if already installed ────────────────────────────────────────────
	existingCfg, _ := k8sinternal.ReadConfig(ctx, k8sClient)
	if existingCfg != nil && !installForce {
		fmt.Println("  tether is already installed.")
		fmt.Printf("  Relay: %s\n", existingCfg.RelayExternal)
		fmt.Println()
		fmt.Println("  Use --force to reinstall (note: this regenerates the PSK and")
		fmt.Println("  invalidates all existing session tokens).")
		fmt.Println()
		fmt.Println("  To intercept a service:")
		fmt.Println("    tether start")
		return nil
	}

	// ── Build image ───────────────────────────────────────────────────────────
	printStep("Building image (linux/amd64)")
	if err := buildImage(ctx, repoRoot); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	printOK("tether:dev built")

	// ── Import image into cluster ─────────────────────────────────────────────
	printStep(fmt.Sprintf("Importing image into %s cluster", ct))
	if err := importImage(ctx, ct, contextName); err != nil {
		return fmt.Errorf("image import: %w", err)
	}
	printOK("Image imported")

	// ── Deploy to cluster ─────────────────────────────────────────────────────
	printStep("Deploying to cluster (namespace: tether)")

	if err := k8sinternal.EnsureNamespace(ctx, k8sClient); err != nil {
		return err
	}
	printOK("Namespace ready")

	// Generate PSK (preserve existing if reinstalling without --force).
	psk := ""
	if existingCfg != nil && existingCfg.PSK != "" {
		psk = existingCfg.PSK
		printOK("Using existing PSK")
	} else {
		psk, err = generatePSK()
		if err != nil {
			return fmt.Errorf("generate PSK: %w", err)
		}
		printOK("PSK generated")
	}

	// Write PSK secret first — the Deployments reference it.
	if err := k8sinternal.WriteConfig(ctx, k8sClient, k8sinternal.TetherConfig{PSK: psk}); err != nil {
		return err
	}

	nodePort, err := k8sinternal.InstallRelay(ctx, k8sClient)
	if err != nil {
		return err
	}
	printOK(fmt.Sprintf("Relay deployed (NodePort: %d)", nodePort))

	if err := k8sinternal.InstallAgent(ctx, k8sClient); err != nil {
		return err
	}
	printOK("Agent deployed")

	// ── Wait for pods ─────────────────────────────────────────────────────────
	fmt.Print("  Waiting for relay... ")
	if err := k8sinternal.WaitForDeployment(ctx, k8sClient, "tether-relay", "app=tether-relay"); err != nil {
		return fmt.Errorf("relay not ready: %w", err)
	}
	fmt.Println("ready")

	fmt.Print("  Waiting for agent... ")
	if err := k8sinternal.WaitForDeployment(ctx, k8sClient, "tether-agent", "app=tether-agent"); err != nil {
		return fmt.Errorf("agent not ready: %w", err)
	}
	fmt.Println("ready")

	// ── Determine external relay address ──────────────────────────────────────
	// ── Determine external relay address ─────────────────────────────────────
	// k3d on macOS: container IPs are not reachable from the host.
	// We use localhost:8080 and manage access via kubectl port-forward.
	// minikube/k3s: node InternalIP + NodePort is directly reachable.
	var (
		relayExternal  string
		usePortForward bool
	)
	if ct == clusterK3d {
		relayExternal = fmt.Sprintf("localhost:%d", nodePort)
		usePortForward = true
		printOK(fmt.Sprintf("Relay accessible via port-forward (localhost:%d)", nodePort))
	} else {
		nodeIP, err := getNodeIP(ctx)
		if err != nil {
			return fmt.Errorf("get node IP: %w", err)
		}
		relayExternal = fmt.Sprintf("%s:%d", nodeIP, nodePort)
		printOK(fmt.Sprintf("Relay external address: %s", relayExternal))
	}

	// Persist full config.
	if err := k8sinternal.WriteConfig(ctx, k8sClient, k8sinternal.TetherConfig{
		RelayExternal:  relayExternal,
		RelayInternal:  k8sinternal.RelayInternalAddr,
		PSK:            psk,
		UsePortForward: usePortForward,
	}); err != nil {
		return err
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	printDivider()
	fmt.Println()
	fmt.Println("  Installation complete!")
	fmt.Println()
	if usePortForward {
		fmt.Printf("  Relay:        localhost:%d (via kubectl port-forward)\n", nodePort)
	} else {
		fmt.Printf("  Relay:        %s\n", relayExternal)
	}
	fmt.Println()
	fmt.Println("  To intercept a service:")
	fmt.Println("    tether start")
	fmt.Println()
	printDivider()
	return nil
}

// ── Cluster detection ─────────────────────────────────────────────────────────

func detectClusterType(contextName, serverURL string) clusterType {
	if strings.HasPrefix(contextName, "minikube") || contextName == "minikube" {
		return clusterMinikube
	}
	if strings.HasPrefix(contextName, "k3d-") {
		return clusterK3d
	}
	host := serverURL
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	host = strings.SplitN(host, ":", 2)[0]
	if host == "localhost" || host == "127.0.0.1" {
		if commandExists("k3s") {
			return clusterK3sLocal
		}
	}
	// Default to k3d for unrecognised local contexts — common for renamed clusters.
	if commandExists("k3d") {
		return clusterK3d
	}
	return clusterUnknown
}

func checkPrerequisites(ct clusterType) error {
	type prereq struct {
		cmd  string
		hint string
	}
	required := []prereq{
		{"docker", "https://docs.docker.com/get-docker/"},
		{"kubectl", "https://kubernetes.io/docs/tasks/tools/"},
	}
	switch ct {
	case clusterK3d:
		required = append(required, prereq{"k3d", "https://k3d.io"})
	case clusterMinikube:
		required = append(required, prereq{"minikube", "https://minikube.sigs.k8s.io"})
	case clusterK3sLocal:
		required = append(required, prereq{"k3s", "https://k3s.io"})
	case clusterUnknown:
		return fmt.Errorf("unrecognised cluster type — supported: minikube, k3d, k3s\n" +
			"  Ensure your kubectl context name starts with 'minikube' or 'k3d-', or k3s is installed locally")
	}

	var missing []string
	for _, p := range required {
		if commandExists(p.cmd) {
			printOK(p.cmd)
		} else {
			fmt.Printf("  ✗ %s — not found\n", p.cmd)
			missing = append(missing, fmt.Sprintf("%s  →  %s", p.cmd, p.hint))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing prerequisites — install the following and retry:\n  - %s",
			strings.Join(missing, "\n  - "))
	}
	return nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ── Docker build ──────────────────────────────────────────────────────────────

func buildImage(ctx context.Context, repoRoot string) error {
	// repoRoot is validated by findRepoRoot() — Dockerfile must exist there.
	cmd := exec.CommandContext(ctx, "docker", "build", // #nosec G204
		"--platform", "linux/amd64",
		"-t", "tether:dev",
		repoRoot,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// validateClusterIdentifier ensures names passed to CLI tools contain only
// safe characters (alphanumeric, hyphens, underscores, dots) to prevent
// command injection (CWE-78 / gosec G204).
func validateClusterIdentifier(s, label string) error {
	if s == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("invalid %s %q: unexpected character %q", label, s, c)
		}
	}
	return nil
}

// ── Image import ──────────────────────────────────────────────────────────────

func importImage(ctx context.Context, ct clusterType, contextName string) error {
	switch ct {
	case clusterMinikube:
		profile := contextName
		if err := validateClusterIdentifier(profile, "minikube profile"); err != nil {
			return err
		}
		// #nosec G204 -- profile validated by validateClusterIdentifier above
		cmd := exec.CommandContext(ctx, "minikube", "image", "load", "tether:dev", "-p", profile)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case clusterK3d:
		clusterName := strings.TrimPrefix(contextName, "k3d-")
		if err := validateClusterIdentifier(clusterName, "k3d cluster name"); err != nil {
			return err
		}
		// #nosec G204 -- clusterName validated by validateClusterIdentifier above
		cmd := exec.CommandContext(ctx, "k3d", "image", "import", "tether:dev", "-c", clusterName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case clusterK3sLocal:
		// docker save | sudo k3s ctr images import -
		save := exec.CommandContext(ctx, "docker", "save", "tether:dev")
		imp := exec.CommandContext(ctx, "sudo", "k3s", "ctr", "images", "import", "-")
		r, w, err := os.Pipe()
		if err != nil {
			return fmt.Errorf("pipe: %w", err)
		}
		save.Stdout = w
		save.Stderr = os.Stderr
		imp.Stdin = r
		imp.Stdout = os.Stdout
		imp.Stderr = os.Stderr
		if err := save.Start(); err != nil {
			_ = r.Close()
			_ = w.Close()
			return fmt.Errorf("docker save: %w", err)
		}
		if err := imp.Start(); err != nil {
			_ = r.Close()
			_ = w.Close()
			return fmt.Errorf("k3s import: %w", err)
		}
		_ = save.Wait()
		_ = w.Close()
		_ = imp.Wait()
		_ = r.Close()
		return nil
	}
	return fmt.Errorf("unsupported cluster type: %s", ct)
}

// ── Node IP ───────────────────────────────────────────────────────────────────

func getNodeIP(ctx context.Context) (string, error) {
	k8s, err := buildK8sClient()
	if err != nil {
		return "", err
	}
	return k8sinternal.GetNodeIP(ctx, k8s)
}

// ── PSK ───────────────────────────────────────────────────────────────────────

func generatePSK() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ── Repo root detection ───────────────────────────────────────────────────────

func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("Dockerfile not found — run 'tether install' from inside the tether repository")
}

// ── Kubeconfig helpers ────────────────────────────────────────────────────────

func getKubeInfo() (contextName, serverURL string, err error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}
	rawCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, overrides,
	).RawConfig()
	if err != nil {
		return "", "", err
	}

	ctx := rawCfg.CurrentContext
	if kubecontext != "" {
		ctx = kubecontext
	}
	ctxObj, ok := rawCfg.Contexts[ctx]
	if !ok {
		return ctx, "", nil
	}
	cluster, ok := rawCfg.Clusters[ctxObj.Cluster]
	if !ok {
		return ctx, "", nil
	}
	return ctx, cluster.Server, nil
}

// ── Print helpers ─────────────────────────────────────────────────────────────

func printHeader(title string) {
	fmt.Println()
	fmt.Printf("  %s\n", title)
	printDivider()
	fmt.Println()
}

func printStep(msg string) {
	fmt.Println()
	fmt.Printf("  ── %s\n", msg)
	fmt.Println()
}

func printOK(msg string) {
	fmt.Printf("  ✓ %s\n", msg)
}

func printDivider() {
	fmt.Println("  ────────────────────────────────────────────────────────")
}
