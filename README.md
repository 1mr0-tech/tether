# tether

Route traffic from a Kubernetes service to your local machine — no VPN, no cluster access for developers.

```
[Caller in cluster] → [k8s Service] → [Agent Pod] → [Relay Server] → [Dev Machine :port]
[Dev Machine :port] → [Relay Server] → [Agent Pod] → [Caller]
```

---

## How it works

Three pieces work together:

1. **Relay server** — a TCP server reachable from both the cluster and developer machines. Can run in-cluster (recommended) or on any host with a stable IP.
2. **Agent pod** — a persistent pod in the `tether` namespace. Installed once per cluster. Dynamically intercepts traffic on demand without restarting.
3. **CLI** — ops uses it to start/stop interceptions. Developers use it to forward intercepted traffic to their local port.

Developers need **zero Kubernetes knowledge** and **zero cluster access**. They only need a session token from ops and the `tether` binary.

---

## For Ops: Install tether on a cluster

Use the interactive installer from inside the tether repo:

```bash
./scripts/cluster-install.sh
```

The script handles everything:

1. **Detects the cluster type** automatically from your kubeconfig — k3d, k3s (local or remote)
2. **Asks where the relay should run:**
   - **In the cluster** — deploys relay as a Deployment with a NodePort service in the `tether` namespace. Recommended for shared dev servers (k3s). Relay is always-on with no local process needed.
   - **Local machine** — relay runs as a `tether server` process on your machine. Recommended for personal k3d setups.
3. **Scans for a free NodePort** (>31000) and lets you override if needed
4. **Builds the Docker image** locally via `docker build`
5. **Imports the image directly** into the cluster — no registry required:
   - k3d: `k3d image import`
   - k3s (local): `sudo k3s ctr images import`
   - k3s (remote): copies via SCP and imports over SSH
6. **Deploys** the relay (if in-cluster) and agent to the `tether` namespace
7. **Prints** the exact `tether start` command to use going forward

### Requirements

- `kubectl` connected to the cluster (`~/.kube/config` or `$KUBECONFIG`)
- `docker` available locally
- For remote k3s: SSH access to the node (key-based or password)
- For k3d: `k3d` CLI installed

### RBAC

The ops user running `tether start` and `tether stop` needs permission to:
- Get/patch Services and Endpoints in target namespaces

A cluster-admin kubeconfig works. For a more restricted setup, scope permissions to the `tether` namespace plus any namespaces you'll intercept.

---

## For Ops: Intercepting a service

### On a shared dev server (recommended)

If tether is installed on a shared server (e.g. the k3s node itself), SSH in and run the interactive intercept script:

```bash
ssh devserver@<server>
tether-intercept
```

The script:
1. Lists available namespaces (filters out system namespaces)
2. Lists deployments in the chosen namespace
3. Runs `tether start` and displays the session token prominently
4. Copies the `tether connect` command to clipboard if available

Share the printed command with the developer.

### Manually (any machine with kubeconfig)

```bash
tether start <deployment-name> -n <namespace> --relay <relay-addr>
```

Example output:
```
Found service backend (targetPort: 8080)
Opening relay session...

Intercepting demo/backend → developer laptop

Share this with the developer:

  tether connect --session eyJpZCI6... --port <local-port>

To stop:  tether stop --session eyJpZCI6...
```

### Stop an interception

```bash
tether stop --session <token>
```

Restores the original service routing immediately.

> **Note:** `tether start` and `tether stop` must be run on the same machine — session state is saved locally at `~/.tether/sessions/`.

---

## For Developers: Connect to an intercepted service

### 1. Install tether (one-time)

```bash
curl -fsSL https://raw.githubusercontent.com/1mr0-tech/tether/main/scripts/install-tether.sh | bash
```

The script detects your OS and architecture, checks for Go (installs it if missing), builds tether from source, and installs the binary to `/usr/local/bin/tether`.

### 2. Connect

Paste the command ops sent you and add `--port` for your local server:

```bash
tether connect --session eyJ... --port 3000
```

That's it. Traffic hitting the intercepted service in the cluster now arrives at `localhost:3000`. Run your local dev server on that port.

Press **Ctrl+C** to disconnect. The cluster service keeps running for others — only your local forwarding stops.

---

## Local development with k3d

Run `./scripts/cluster-install.sh`, choose **"Local machine"** for the relay, and the script configures everything automatically:

- Agent pods connect to the relay via `host.docker.internal`
- Ops and developers connect via your detected LAN IP

Then start the relay before intercepting:

```bash
# Terminal 1: start relay
tether server --addr :8080

# Terminal 2: intercept a service
tether start backend -n demo --relay <your-lan-ip>:8080

# Developer machine: connect
tether connect --session eyJ... --port 3000
```

---

## Crash recovery

If `tether stop` was never run (CLI crashed, machine rebooted), session state is saved at:

```
~/.tether/sessions/<session-id>.json
```

Run `tether stop` with the original token — it loads state from disk and restores the service:

```bash
tether stop --session <token>
```

To find active sessions, check which services have a missing selector:

```bash
kubectl get svc -n <namespace> -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.selector}{"\n"}{end}'
# A service with empty selector output is currently being intercepted
```

---

## How traffic interception works

1. `tether start` finds the Service targeting the Deployment, reads its `spec.selector` and `targetPort`, saves them to `~/.tether/sessions/<id>.json`.
2. The service's `spec.selector` is removed and manual `Endpoints` are created pointing to the always-on agent pod IP.
3. A control message is sent to the agent pod via the relay: *"listen on port `<targetPort>` for session `<id>`"*.
4. The agent starts listening on that port and opens a yamux tunnel to the relay.
5. When the developer runs `tether connect`, it connects to the relay from the other side, accepting yamux streams and forwarding each to `localhost:<local-port>`.
6. For every inbound TCP connection the agent receives, it opens a yamux stream through the relay. The developer's CLI accepts the stream and dials the local port.
7. On `tether stop`: original selector is restored, manual Endpoints deleted, agent port released.

The relay is protocol-agnostic — it forwards raw bytes. HTTP, gRPC, WebSockets, and plain TCP all work.

---

## Security notes

- **Session tokens** encode a 128-bit cryptographically random session ID. They act as an unguessable shared secret between agent and developer CLI.
- **The relay has no authentication** beyond session IDs. Run it on a private network or restrict inbound access to developer and cluster IPs.
- TLS on the relay connection is planned for a future release.

---

## Troubleshooting

**Agent pod not connecting to relay**
```bash
kubectl logs -n tether -l app=tether-agent
```
Most common cause: the relay address is unreachable from inside the cluster. Verify the relay address and port from a pod in the cluster.

**Service gets no traffic after `tether start`**
```bash
kubectl get endpoints -n <namespace> <service-name>
```
The endpoint should show the agent pod IP. If empty, check that the agent pod is running:
```bash
kubectl get pods -n tether
```

**Developer can't connect**
```bash
# Check relay is reachable
nc -zv <relay-host> <relay-port>
```
If connection times out, check firewall rules between the developer's machine and the relay.

**Service is stuck with no selector after a crash**

If you have the token:
```bash
tether stop --session <token>
```

If you don't have the token, restore manually with kubectl:
```bash
kubectl patch svc <name> -n <namespace> --type=merge \
  -p '{"spec":{"selector":{"app":"<original-label-value>"}}}'
kubectl delete endpoints <name> -n <namespace>
```

---

## Repository

[https://github.com/1mr0-tech/tether](https://github.com/1mr0-tech/tether)
