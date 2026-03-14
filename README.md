# tether

Route live Kubernetes service traffic to your local machine — no VPN, no cluster access required for developers.

```
[In-cluster caller] → [k8s Service] → [Agent Pod] → [Relay Server] → [Dev Machine :port]
```

---

## How it works

Three components work together:

1. **Relay** — a TCP server deployed in-cluster. Bridges the agent and developer connections.
2. **Agent** — a persistent pod in the `tether` namespace, installed once per cluster. Intercepts service traffic on demand.
3. **CLI** — ops uses it to install, start, and stop interceptions. Developers use it to forward intercepted traffic to a local port.

Developers need **zero Kubernetes knowledge** and **zero cluster access**. They only need a session token from ops and the `tether` binary.

---

## Installation

Run once per cluster from inside the tether repository:

```bash
tether install
```

The installer:
- Detects your cluster type (k3d, minikube, k3s) automatically from kubeconfig
- Builds the Docker image locally and imports it directly — no registry required
- Deploys the relay and agent to the `tether` namespace
- Generates a pre-shared key and stores it in a k8s Secret
- Persists the relay address to a ConfigMap so subsequent commands need no flags

**Requirements:** `docker`, `kubectl`, and (depending on cluster type) `k3d` or `minikube` in PATH.

---

## Intercepting a service (Ops)

```bash
tether start
```

Interactive — lists available namespaces and deployments to pick from. Outputs a session token and the exact command to share with the developer.

To stop and restore the original service:

```bash
tether stop --session <token>
```

---

## Connecting to an intercepted service (Developer)

Paste the command ops sent you, adding `--port` for your local server:

```bash
tether connect --session <token> --port 9090
```

All traffic hitting the intercepted k8s service now arrives at `localhost:9090`. Press **Ctrl+C** to disconnect. The cluster service is unaffected and keeps running for others.

---

## k3d (local development)

tether handles k3d automatically. The relay NodePort is not directly reachable from the host, so tether manages `kubectl port-forward` transparently on `start`, `connect`, and `stop` — no manual steps needed.

---

## Supported cluster types

| Cluster | Status |
|---------|--------|
| k3d | Fully supported |
| minikube | Fully supported |
| k3s (local) | Fully supported |
| GKE / AKS / EKS | Coming soon |

---

## Security

- **Pre-shared key (PSK):** a 32-byte cryptographically random key is generated at install time, stored in a k8s Secret, and validated on every relay connection. Connections with an incorrect or missing PSK are rejected immediately.
- **Session token:** an opaque base64 string containing the session ID, relay address, and PSK. Treat it as a secret — share only with the developer who needs it.
- **Container hardening:** relay and agent pods run as non-root (UID 65532), with a read-only filesystem, no privilege escalation, and all capabilities dropped.
- **No TLS on relay connections:** traffic is authenticated via PSK but not encrypted in transit. Recommended for use on a private network or VPN.

---

## How traffic interception works

1. `tether start` locates the Service targeting the chosen Deployment, reads its selector and targetPort.
2. The service's selector is removed and manual Endpoints are created pointing at the agent pod IP.
3. Stale EndpointSlices (which k8s does not remove automatically) are deleted to prevent traffic splitting.
4. A control message is sent to the agent: open a listener on `targetPort` for this session.
5. The agent listens on that port, opens a yamux tunnel through the relay for the session.
6. `tether connect` connects to the relay from the developer's side, accepts yamux streams, and forwards each to `localhost:<local-port>`.
7. `tether stop` restores the original selector, removes manual Endpoints, and signals the agent to release the port.

The relay is protocol-agnostic — it forwards raw bytes. HTTP, gRPC, WebSockets, and plain TCP all work.

---

## Crash recovery

If `tether stop` was never run (crash, reboot), session state is saved at `~/.tether/sessions/<id>.json`. Run `tether stop` with the original token to restore:

```bash
tether stop --session <token>
```

To identify intercepted services manually:

```bash
kubectl get svc -n <namespace> -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.selector}{"\n"}{end}'
# A service with an empty selector is currently intercepted
```

To restore without the token:

```bash
kubectl patch svc <name> -n <namespace> --type=merge \
  -p '{"spec":{"selector":{"app":"<original-value>"}}}'
kubectl delete endpoints <name> -n <namespace>
```

---

## Troubleshooting

**Agent not connecting to relay**
```bash
kubectl logs -n tether -l app=tether-agent
```

**Service gets no traffic after `tether start`**
```bash
kubectl get endpoints -n <namespace> <service-name>
# Should show the agent pod IP
kubectl get pods -n tether
```

**Developer can't connect**
```bash
nc -zv <relay-host> <relay-port>
```
Check firewall rules between the developer's machine and the relay.

---

## Documentation

Full usage guide, installation walkthroughs, and command reference: [DOCUMENTATION.md](DOCUMENTATION.md)

---

## Repository

[https://github.com/1mr0-tech/tether](https://github.com/1mr0-tech/tether)
