# tether

Route traffic from a Kubernetes service to your local machine — no VPN, no cluster access for developers.

```
[Caller in cluster] → [k8s Service] → [Agent Pod] → [Relay Server] → [Dev Machine :port]
[Dev Machine :port] → [Relay Server] → [Agent Pod] → [Caller]
```

---

## How it works

Three pieces work together:

1. **Relay server** — a TCP server with a stable IP reachable from both the cluster and developer machines. Ops deploys this once.
2. **Agent pod** — a persistent pod in the `tether` namespace. Ops installs this once per cluster. It dynamically intercepts traffic on demand.
3. **CLI** — ops uses it to start/stop interceptions. Developers use it to forward traffic to their local port.

Developers need **zero Kubernetes knowledge** and **zero cluster access**. They only need a session token from ops and the `tether` binary.

---

## For Ops: One-time Setup

### 1. Deploy the relay server

The relay must be reachable from both the cluster and developer machines. Run it on any host with a stable IP or hostname.

```bash
tether server --addr :8080
```

To run it as a systemd service, Docker container, or in-cluster LoadBalancer — any approach works as long as it stays up and both sides can reach it.

### 2. Install the agent into the cluster

```bash
tether install
```

This is interactive. It asks:
- Whether the relay is on localhost (for local k3d/k3s development) or an external address
- The relay address and port
- The agent container image to use

It then deploys a persistent `tether-agent` pod in the `tether` namespace. This pod stays running and handles all future interceptions — no per-session deployments.

**Build and push the agent image first:**
```bash
docker build -t your-registry/tether:latest .
docker push your-registry/tether:latest
```

### 3. RBAC for ops

The ops user running `tether install` and `tether start` needs permission to:
- Create namespaces (for the `tether` namespace)
- Create Deployments in the `tether` namespace
- Get/patch Services and Endpoints in the target namespaces

A cluster-admin kubeconfig works. For a more restricted setup, scope permissions to the `tether` namespace plus any namespaces you'll intercept.

---

## For Ops: Intercepting a Service

### Start an interception

```bash
tether start <deployment-name> -n <namespace> --relay <relay-addr>
```

This finds the Service that targets the given Deployment, redirects its traffic to the agent pod, and prints a ready-to-run command for the developer:

```
$ tether start backend -n demo --relay relay.company.internal:8080

Session token: eyJpZCI6IjRmOWEyYzFkOGUzYjdmMDUiLCJyZWxheSI6InJlbGF5LmNvbXBhbnkuaW50ZXJuYWw6ODA4MCJ9

Run this on the developer machine:
  tether connect --session eyJpZCI6IjRmOWEyYzFkOGUzYjdmMDUiLCJyZWxheSI6InJlbGF5LmNvbXBhbnkuaW50ZXJuYWw6ODA4MCJ9 --port <local-port>
```

Share the printed `tether connect` command with the developer — they paste it in and add their local port number.

### Stop an interception

```bash
tether stop --session <token>
```

This restores the original service routing and releases the port on the agent pod.

---

## For Developers: Connect to an intercepted service

You need two things from ops:
- The `tether` binary installed on your machine
- The session token (a `tether connect` command from ops)

### Install the CLI

**macOS / Linux — build from source (requires Go 1.24+):**
```bash
git clone https://github.com/your-org/tether
cd tether
go build -o tether .
sudo mv tether /usr/local/bin/
```

**Or download a pre-built binary** (when releases are published):
```bash
# macOS arm64
curl -fsSL https://github.com/your-org/tether/releases/latest/download/tether-darwin-arm64 \
  -o /usr/local/bin/tether && chmod +x /usr/local/bin/tether
```

### Connect

Paste the command that ops gave you and add `--port` for your local server:

```bash
tether connect --session eyJ... --port 3000
```

That's it. Traffic hitting the intercepted service in the cluster now arrives at `localhost:3000` on your machine. Run your local dev server on that port.

Press **Ctrl+C** to disconnect. The service keeps running for other developers — only your local forwarding stops.

---

## Local development with k3d

When the relay runs on your laptop alongside k3d, use the interactive installer — it automatically configures the correct address for pods to reach the relay via `host.docker.internal`.

```bash
# Terminal 1: start relay
tether server --addr :8085

# Terminal 2: install agent (choose "localhost" when prompted)
tether install

# Terminal 3: start interception
tether start backend -n demo --relay localhost:8085

# Terminal 4: connect (as developer)
tether connect --session eyJ... --port 3000
```

---

## Crash recovery

If `tether stop` was never run (CLI crashed, power loss), session state is saved at:

```
~/.tether/sessions/<session-id>.json
```

Restore manually:

```bash
tether stop --session <token>
```

If you have the token, the above will work even after a crash — it loads state from disk and restores the service.

To find active sessions, check which services have a missing selector:
```bash
kubectl get svc -n <namespace> -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.selector}{"\n"}{end}'
# A service with no selector output is being intercepted
```

---

## How traffic interception works (under the hood)

1. `tether start` finds the Service that routes to the target Deployment, reads its `spec.selector` and `targetPort`, and saves them to `~/.tether/sessions/<id>.json`.
2. The service's `spec.selector` is removed and manual `Endpoints` are created pointing to the always-on agent pod IP (cross-namespace).
3. A control message is sent to the agent pod via the relay: "listen on port `<targetPort>` for session `<id>`".
4. The agent pod starts listening on that port and opens a yamux tunnel to the relay.
5. When the developer runs `tether connect`, it connects to the relay from the other side, accepting yamux streams and forwarding each one to `localhost:<local-port>`.
6. For every inbound TCP connection the agent receives, it opens a yamux stream through the relay. The developer's CLI accepts the stream and dials their local port.
7. On `tether stop`: original selector is restored, manual Endpoints deleted, and the agent port is released.

The relay is protocol-agnostic — it forwards raw bytes. HTTP, gRPC, WebSockets, and plain TCP all work.

---

## Security notes

- **Session tokens** encode a 128-bit cryptographically random session ID. They act as an unguessable shared secret between agent and CLI.
- **The relay has no authentication** beyond session IDs. Run it on a private network or restrict inbound access to developer and cluster IPs.
- TLS on the relay connection is planned for a future release.

---

## Troubleshooting

**Agent pod not found after `tether install`**
```bash
kubectl get pods -n tether
kubectl logs -n tether -l app=tether-agent
```
Most common cause: the agent can't reach the relay. Verify the relay address resolves from inside the cluster.

**Service gets no traffic after `tether start`**
```bash
kubectl get endpoints -n <namespace> <service-name>
```
The endpoint should show the agent pod IP. If empty, the agent pod IP may have changed since install — re-run `tether install`.

**Developer can't connect**
```bash
# Check relay is running
nc -zv <relay-host> <relay-port>

# Check session token is valid
tether connect --session <token> --port 9999
```
If connection times out, the relay is unreachable. Check firewall rules.

**Service is stuck with no selector**
```bash
tether stop --session <token>
```
If you don't have the token but have the session ID:
```bash
# The session ID is the file in ~/.tether/sessions/
ls ~/.tether/sessions/
```
Then reconstruct the token or restore the service directly with kubectl:
```bash
kubectl patch svc <name> -n <namespace> --type=merge -p '{"spec":{"selector":{"app":"<original-label-value>"}}}'
kubectl delete endpoints <name> -n <namespace>
```
