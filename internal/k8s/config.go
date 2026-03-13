package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	configMapName      = "tether-config"
	pskSecretName      = "tether-psk"
	keyRelayExternal   = "relay-external"   // host:port for ops CLI and developers
	keyRelayInternal   = "relay-internal"   // ClusterIP service for the agent pod
	keyPSK             = "psk"
	keyUsePortForward  = "use-port-forward" // "true" when kubectl port-forward is needed (k3d/macOS)
)

// TetherConfig holds the addresses and PSK written at install time.
type TetherConfig struct {
	RelayExternal   string // e.g. "10.89.0.2:31000" or "localhost:8080" for k3d
	RelayInternal   string // e.g. "tether-relay.tether.svc.cluster.local:8080"
	PSK             string // hex-encoded 32-byte random key
	UsePortForward  bool   // true for k3d on macOS — relay needs kubectl port-forward
}

// WriteConfig persists the relay addresses and PSK to the cluster.
// ConfigMap holds addresses; Secret holds the PSK.
func WriteConfig(ctx context.Context, client kubernetes.Interface, cfg TetherConfig) error {
	pfVal := "false"
	if cfg.UsePortForward {
		pfVal = "true"
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: agentNamespace},
		Data: map[string]string{
			keyRelayExternal:  cfg.RelayExternal,
			keyRelayInternal:  cfg.RelayInternal,
			keyUsePortForward: pfVal,
		},
	}
	_, err := client.CoreV1().ConfigMaps(agentNamespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && isAlreadyExists(err) {
		_, err = client.CoreV1().ConfigMaps(agentNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("write tether-config: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: pskSecretName, Namespace: agentNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{keyPSK: []byte(cfg.PSK)},
	}
	_, err = client.CoreV1().Secrets(agentNamespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && isAlreadyExists(err) {
		_, err = client.CoreV1().Secrets(agentNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("write tether-psk secret: %w", err)
	}
	return nil
}

// ReadConfig reads the relay configuration written by 'tether install'.
func ReadConfig(ctx context.Context, client kubernetes.Interface) (*TetherConfig, error) {
	cm, err := client.CoreV1().ConfigMaps(agentNamespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("tether not installed — run 'tether install' first")
	}
	secret, err := client.CoreV1().Secrets(agentNamespace).Get(ctx, pskSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("tether PSK secret missing — run 'tether install' first")
	}
	cfg := &TetherConfig{
		RelayExternal:  cm.Data[keyRelayExternal],
		RelayInternal:  cm.Data[keyRelayInternal],
		PSK:            string(secret.Data[keyPSK]),
		UsePortForward: cm.Data[keyUsePortForward] == "true",
	}
	if cfg.RelayExternal == "" || cfg.PSK == "" {
		return nil, fmt.Errorf("tether config is incomplete — run 'tether install' first")
	}
	return cfg, nil
}
