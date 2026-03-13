package k8s

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	relayDeploymentName = "tether-relay"
	relayServiceName    = "tether-relay"
	// RelayInternalAddr is the ClusterIP address agents use to reach the relay.
	RelayInternalAddr   = "tether-relay.tether.svc.cluster.local:8080"
	agentDeploymentName = "tether-agent"
	imageRef            = "docker.io/library/tether:dev"
)

// EnsureNamespace creates the tether namespace if it does not exist.
func EnsureNamespace(ctx context.Context, client kubernetes.Interface) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNamespace}}
	_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("create namespace: %w", err)
	}
	return nil
}

// InstallRelay deploys the relay Deployment and NodePort Service in the tether namespace.
// It returns the auto-assigned NodePort.
func InstallRelay(ctx context.Context, client kubernetes.Interface) (int32, error) {
	// Deploy relay Deployment.
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      relayDeploymentName,
			Namespace: agentNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": relayDeploymentName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": relayDeploymentName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "relay",
							Image:           imageRef,
							ImagePullPolicy: corev1.PullNever,
							Args:            []string{"server", "--addr", ":8080"},
							Ports: []corev1.ContainerPort{
								{Name: "relay", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{
									Name: "RELAY_PSK",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: pskSecretName},
											Key:                  keyPSK,
										},
									},
								},
							},
							Resources:       secureResources(),
							SecurityContext: secureContainerCtx(),
						},
					},
					SecurityContext: securePodCtx(),
				},
			},
		},
	}

	_, err := client.AppsV1().Deployments(agentNamespace).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil && !isAlreadyExists(err) {
		return 0, fmt.Errorf("create relay deployment: %w", err)
	}
	if isAlreadyExists(err) {
		_, err = client.AppsV1().Deployments(agentNamespace).Update(ctx, dep, metav1.UpdateOptions{})
		if err != nil {
			return 0, fmt.Errorf("update relay deployment: %w", err)
		}
	}

	// Deploy NodePort Service — let k8s auto-assign the port.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      relayServiceName,
			Namespace: agentNamespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{"app": relayDeploymentName},
			Ports: []corev1.ServicePort{
				{
					Name:     "relay",
					Port:     8080,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}

	created, err := client.CoreV1().Services(agentNamespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && isAlreadyExists(err) {
		// Read existing to get the assigned NodePort.
		created, err = client.CoreV1().Services(agentNamespace).Get(ctx, relayServiceName, metav1.GetOptions{})
	}
	if err != nil {
		return 0, fmt.Errorf("create relay service: %w", err)
	}

	nodePort := created.Spec.Ports[0].NodePort
	log.Printf("relay service NodePort: %d", nodePort)
	return nodePort, nil
}

// InstallAgent deploys the always-on agent Deployment in the tether namespace.
func InstallAgent(ctx context.Context, client kubernetes.Interface) error {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentDeploymentName,
			Namespace: agentNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": agentDeploymentName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": agentDeploymentName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "agent",
							Image:           imageRef,
							ImagePullPolicy: corev1.PullNever,
							Args:            []string{"agent"},
							Env: []corev1.EnvVar{
								{Name: "RELAY_ADDR", Value: RelayInternalAddr},
								{
									Name: "RELAY_PSK",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: pskSecretName},
											Key:                  keyPSK,
										},
									},
								},
							},
							Resources:       secureResources(),
							SecurityContext: secureContainerCtx(),
						},
					},
					SecurityContext: securePodCtx(),
				},
			},
		},
	}

	_, err := client.AppsV1().Deployments(agentNamespace).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil && isAlreadyExists(err) {
		_, err = client.AppsV1().Deployments(agentNamespace).Update(ctx, dep, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("create agent deployment: %w", err)
	}
	return nil
}

// WaitForDeployment polls until all pods in a deployment are ready.
func WaitForDeployment(ctx context.Context, client kubernetes.Interface, name, labelSelector string) error {
	pollCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	return wait.PollUntilContextTimeout(pollCtx, 2*time.Second, 120*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := client.CoreV1().Pods(agentNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

// GetNodeIP returns the InternalIP of the first cluster node.
func GetNodeIP(ctx context.Context, client kubernetes.Interface) (string, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("no nodes found in cluster")
	}
	for _, addr := range nodes.Items[0].Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("could not determine node IP")
}

// secureResources returns standard resource requests/limits for tether pods.
func secureResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
}

// secureContainerCtx returns a hardened container security context.
func secureContainerCtx() *corev1.SecurityContext {
	allowPrivEsc := false
	readOnly := true
	nonRoot := true
	uid := int64(65532) // distroless nonroot UID
	return &corev1.SecurityContext{
		RunAsNonRoot:             &nonRoot,
		RunAsUser:                &uid,
		ReadOnlyRootFilesystem:   &readOnly,
		AllowPrivilegeEscalation: &allowPrivEsc,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// securePodCtx returns a hardened pod-level security context.
func securePodCtx() *corev1.PodSecurityContext {
	nonRoot := true
	uid := int64(65532)
	return &corev1.PodSecurityContext{
		RunAsNonRoot: &nonRoot,
		RunAsUser:    &uid,
	}
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}
