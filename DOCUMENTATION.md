# tether

Route live Kubernetes service traffic to your laptop for local development and debugging.

---

## What is tether?

When debugging a microservice in a running Kubernetes cluster, you typically face two bad options: deploy a debug image to the cluster (slow, pollutes the cluster) or reproduce the issue entirely on your laptop without real cluster traffic (misses real dependencies and data).

tether solves this by intercepting a Kubernetes service's traffic at the network layer and forwarding it to a port on your laptop — in real time, with no changes to your application code or cluster workloads. When you are done, one command restores everything exactly as it was.

```
[Caller] → [k8s Service] → [Agent Pod] → [Relay] → [tether connect] → [localhost:PORT]
```

- Traffic from the rest of the cluster reaches your service as normal
- Your local process receives it and handles it
- Other services in the cluster receive responses from your local process
- When you stop: zero cluster changes remain

---

## How it works

### Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Kubernetes cluster                                      │
│                                                          │
│  frontend-pod ──► backend-pod ──► database-api Service   │
│                                         │                │
│                               ┌─────────▼──────────┐    │
│                               │  tether-agent pod  │    │
│                               │  (tether namespace)│    │
│                               │  :8080 listener    │    │
│                               └─────────┬──────────┘    │
│                                         │ yamux          │
│                               ┌─────────▼──────────┐    │
│                               │  tether-relay pod  │    │
│                               │  (tether namespace)│    │
│                               └─────────┬──────────┘    │
└─────────────────────────────────────────┼───────────────┘
                                          │ TCP
                              ┌───────────▼────────────┐
                              │  tether connect (CLI)  │
                              │  developer's laptop    │
                              └───────────┬────────────┘
                                          │
                              ┌───────────▼────────────┐
                              │  localhost:PORT         │
                              │  your dev process      │
                              └────────────────────────┘
```

| Component | Role |
|-----------|------|
| `tether-relay` | In-cluster TCP relay. Bridges the agent's data connection with the developer's connection. Deployed as a Deployment + NodePort Service in the `tether` namespace. |
| `tether-agent` | Always-on pod in the `tether` namespace. Maintains a persistent control channel to the relay. Opens a TCP listener on the service's target port for each active session. |
| `tether start` | Ops command. Patches the k8s Service endpoints to point at the agent pod, signals the agent to open a session, and prints a token for the developer. |
| `tether connect` | Developer command. Connects to the relay using the session token and forwards all traffic to a local port. Requires zero Kubernetes knowledge. |
| `tether stop` | Ops command. Restores the original service endpoints and closes the session. |

### Interception mechanism

When `tether start` runs for a service:

1. The service's `spec.selector` is set to `null`, stopping Kubernetes from managing its endpoints
2. The Endpoints object is updated to point directly to the agent pod's IP and the service's target port
3. Stale selector-managed EndpointSlices are removed to ensure kube-proxy routes 100% of traffic to the agent

This approach works cross-namespace and is compatible with any Kubernetes service type.

### Traffic multiplexing

The relay does raw TCP bridging. Multiplexing is handled end-to-end with [yamux](https://github.com/hashicorp/yamux): the agent (yamux client) opens a new stream for every inbound connection on the session port; the developer CLI (yamux server) accepts each stream and dials `localhost:PORT`. A single relay connection handles any number of concurrent in-flight requests.

### Security

A 32-byte random PSK is generated at install time and stored in a Kubernetes Secret (`tether-psk` in the `tether` namespace). Every connection to the relay — agent, ops, and developer CLI — must present this PSK in the handshake or it is silently dropped. The PSK is embedded in the opaque session token so the developer never needs to configure it manually.

---

## Prerequisites

| Requirement | Notes |
|-------------|-------|
| Go 1.22+ | For building the tether binary |
| Docker | For building the in-cluster image |
| `kubectl` | Configured to your target cluster |
| k3d **or** minikube **or** k3s | Supported local cluster types |

> **Cloud clusters (GKE, AKS, EKS, etc.)** — support is coming soon.

---

## Building tether

Clone the repository and build the binary:

```bash
git clone https://github.com/1mr0-tech/tether
cd tether
go build -o tether .
```

Optionally move the binary to your PATH:

```bash
sudo mv tether /usr/local/bin/tether
```

---

## Installation

`tether install` must be run from the tether repository root (it builds the Docker image from the Dockerfile in the current directory). It detects your cluster type automatically from the active kubectl context and handles everything:

- Builds `tether:dev` as a `linux/amd64` Docker image
- Imports the image into the cluster — no registry required
- Creates the `tether` namespace
- Generates a PSK and stores it in a Kubernetes Secret
- Deploys `tether-relay` (Deployment + NodePort Service) and `tether-agent` (Deployment)
- Waits for both pods to be ready
- Stores the relay address and PSK in a ConfigMap so `tether start` needs no flags

### k3d

k3d is detected when the kubectl context name starts with `k3d-` (the default when you run `k3d cluster create`).

```bash
# Create a cluster if you don't have one
k3d cluster create my-cluster --agents 1

# Install tether (run from the tether repo root)
tether install
```

**Example output:**

```
$ tether install

  tether installer
  ────────────────────────────────────────────────────────

  Context:  k3d-my-cluster
  Cluster:  k3d


  ── Checking prerequisites

  ✓ docker
  ✓ kubectl
  ✓ k3d

  ── Building image (linux/amd64)

#0 building with "default" instance using docker driver
...
#12 [builder 6/6] RUN CGO_ENABLED=0 GOOS=linux go build -o tether .
#12 DONE 84.0s
...
  ✓ tether:dev built

  ── Importing image into k3d cluster

INFO Importing image(s) into cluster 'my-cluster'
INFO Saving 1 image(s) from runtime...
INFO Importing images from tarball into node 'k3d-my-cluster-agent-0'...
INFO Importing images from tarball into node 'k3d-my-cluster-server-0'...
INFO Successfully imported image(s)
  ✓ Image imported

  ── Deploying to cluster (namespace: tether)

  ✓ Namespace ready
  ✓ PSK generated
  ✓ Relay deployed (NodePort: 31986)
  ✓ Agent deployed
  Waiting for relay... ready
  Waiting for agent... ready
  ✓ Relay accessible via port-forward (localhost:31986)
  ────────────────────────────────────────────────────────

  Installation complete!

  Relay:        localhost:31986 (via kubectl port-forward)

  To intercept a service:
    tether start

  ────────────────────────────────────────────────────────
```

On k3d the relay NodePort is not directly reachable from the host (the cluster runs inside Docker containers). tether automatically manages `kubectl port-forward` for every command that needs to reach the relay (`tether start`, `tether connect`, `tether stop`), so you never need to run port-forward manually.

### minikube

minikube is detected when the kubectl context name is `minikube` or starts with `minikube`.

```bash
# Start minikube if not already running
minikube start

# Install tether (run from the tether repo root)
tether install
```

**Example output:**

```
$ tether install

  tether installer
  ────────────────────────────────────────────────────────

  Context:  minikube
  Cluster:  minikube


  ── Checking prerequisites

  ✓ docker
  ✓ kubectl
  ✓ minikube

  ── Building image (linux/amd64)

  ...
  ✓ tether:dev built

  ── Importing image into minikube cluster

  ✓ Image imported

  ── Deploying to cluster (namespace: tether)

  ✓ Namespace ready
  ✓ PSK generated
  ✓ Relay deployed (NodePort: 32045)
  ✓ Agent deployed
  Waiting for relay... ready
  Waiting for agent... ready
  ✓ Relay external address: 192.168.49.2:32045
  ────────────────────────────────────────────────────────

  Installation complete!

  Relay:        192.168.49.2:32045

  To intercept a service:
    tether start

  ────────────────────────────────────────────────────────
```

On minikube the relay NodePort is directly reachable via the minikube node IP. No port-forward is needed.

### k3s (Linux)

> k3s runs on Linux only. These instructions apply to a local k3s installation on a Linux machine.

k3s is detected when the kubectl context server URL is `https://localhost:6443` or `https://127.0.0.1:6443` and the `k3s` binary is present in PATH.

**Set up kubeconfig access:**

k3s writes its kubeconfig to `/etc/rancher/k3s/k3s.yaml` (owned by root). Make it accessible to your user:

```bash
# Option A: copy to your home directory
mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $USER ~/.kube/config

# Option B: set the environment variable
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
```

**Install tether:**

```bash
# Run from the tether repo root
tether install
```

**Example output:**

```
$ tether install

  tether installer
  ────────────────────────────────────────────────────────

  Context:  default
  Cluster:  k3s-local


  ── Checking prerequisites

  ✓ docker
  ✓ kubectl
  ✓ k3s

  ── Building image (linux/amd64)

  ...
  ✓ tether:dev built

  ── Importing image into k3s-local cluster

  ✓ Image imported

  ── Deploying to cluster (namespace: tether)

  ✓ Namespace ready
  ✓ PSK generated
  ✓ Relay deployed (NodePort: 31452)
  ✓ Agent deployed
  Waiting for relay... ready
  Waiting for agent... ready
  ✓ Relay external address: 10.0.0.5:31452
  ────────────────────────────────────────────────────────

  Installation complete!

  Relay:        10.0.0.5:31452

  To intercept a service:
    tether start

  ────────────────────────────────────────────────────────
```

The image is imported via `docker save | sudo k3s ctr images import -`, so Docker must be installed and the user needs `sudo` access.

On k3s the relay NodePort is directly reachable via the node's IP. No port-forward is needed.

### Verifying installation

After any install, confirm both pods are running:

```bash
$ kubectl get pods -n tether
NAME                            READY   STATUS    RESTARTS   AGE
tether-agent-5597c68bb9-xm9ws   1/1     Running   0          1m
tether-relay-dd8469cf7-56dhc    1/1     Running   0          1m
```

### Re-running install

If tether is already installed, `tether install` will tell you and exit without making changes:

```
$ tether install

  ...
  tether is already installed.
  Relay: localhost:31986

  Use --force to reinstall (note: this regenerates the PSK and
  invalidates all existing session tokens).

  To intercept a service:
    tether start
```

To reinstall from scratch (e.g. after updating the tether binary):

```bash
tether install --force
```

`--force` rebuilds the image, reimports it, redeploys both pods, and regenerates the PSK. Any session tokens issued before the reinstall become invalid.

---

## Usage

tether has two distinct roles: **ops** (someone with kubectl access who sets up the intercept) and **developer** (who runs their code locally and receives the traffic). In many teams these are the same person.

### Intercepting a service

#### Step 1 — Ops: start the interception

Run `tether start` from any machine with kubectl access to the cluster:

```
$ tether start

Starting kubectl port-forward to relay (localhost:31986)...

Available namespaces:
  1) default

Select namespace [1-1]: 1

Deployments in "default":
  1) backend
  2) database-api
  3) frontend

Select deployment [1-3]: 2

Found service database-api (targetPort: 8080)
Opening relay session...

Intercepting default/database-api → developer laptop

Share this command with the developer:

  tether connect --session eyJpZCI6Ijg0NGMyZjZh... --port <local-port>

To stop:  tether stop --session eyJpZCI6Ijg0NGMyZjZh...
```

You can skip the interactive prompts by passing the deployment name and namespace directly:

```bash
tether start -n default database-api
```

The session token printed at the end is an opaque value containing everything needed to connect: the relay address, session ID, and PSK. Copy the full `tether connect` line and send it to the developer.

#### Step 2 — Developer: connect

The developer runs the `tether connect` command on their machine, replacing `<local-port>` with the port their local service is listening on:

```
$ tether connect \
    --session eyJpZCI6Ijg0NGMyZjZh... \
    --port 9090

Forwarding from 127.0.0.1:31986 -> 8080
Forwarding from [::1]:31986 -> 8080
Connected. Forwarding traffic to localhost:9090
Press Ctrl+C to disconnect.
```

On k3d clusters tether automatically manages `kubectl port-forward` — the developer only needs `kubectl` installed; no cluster configuration is required beyond the session token.

Traffic from the cluster now flows to `localhost:9090` on the developer's machine.

#### Step 3 — Ops: stop the interception

```
$ tether stop --session eyJpZCI6Ijg0NGMyZjZh...

Restoring default/database-api...
Done. default/database-api is back to its original pods.
```

The k8s service is restored to its original pods immediately. The developer's `tether connect` process exits on its own once the session is closed.

---

### Session recovery

tether saves session state to `~/.tether/sessions/<id>.json` when `tether start` runs. If something goes wrong before `tether stop` is called — a crash, a lost terminal, or a misplaced token — the service can still be restored using the token from the saved state file or the original printed output. `tether stop` reads the session state file to know which service to restore; it does not depend on `tether connect` having run at all.

---

### Using a specific cluster or kubeconfig

All commands accept global flags to override the default kubeconfig and context:

```bash
# Use a specific kubeconfig file
tether start --kubeconfig /path/to/kubeconfig

# Use a specific context from your kubeconfig
tether start --context staging-cluster

# Combine both
tether start --kubeconfig /path/to/kubeconfig --context staging-cluster
```

---

## Complete walkthrough (k3d)

This section shows a full test on a local k3d cluster using a three-tier demo application built into the tether image: `frontend → backend → database-api`.

### 1. Create cluster and install tether

```bash
k3d cluster create tether-test --agents 1
tether install
```

### 2. Deploy the demo application

```bash
kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: database-api
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: database-api
  template:
    metadata:
      labels:
        app: database-api
    spec:
      containers:
      - name: database-api
        image: docker.io/library/tether:dev
        imagePullPolicy: Never
        args: ["demo-server"]
        env:
        - {name: DEMO_MODE, value: "database-api"}
        - {name: DEMO_PORT, value: "8080"}
---
apiVersion: v1
kind: Service
metadata:
  name: database-api
  namespace: default
spec:
  selector:
    app: database-api
  ports:
  - {port: 80, targetPort: 8080}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
  template:
    metadata:
      labels:
        app: backend
    spec:
      containers:
      - name: backend
        image: docker.io/library/tether:dev
        imagePullPolicy: Never
        args: ["demo-server"]
        env:
        - {name: DEMO_MODE, value: "backend"}
        - {name: DEMO_PORT, value: "8080"}
        - {name: DEMO_UPSTREAM, value: "http://database-api.default.svc.cluster.local"}
---
apiVersion: v1
kind: Service
metadata:
  name: backend
  namespace: default
spec:
  selector:
    app: backend
  ports:
  - {port: 80, targetPort: 8080}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: frontend
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: frontend
  template:
    metadata:
      labels:
        app: frontend
    spec:
      containers:
      - name: frontend
        image: docker.io/library/tether:dev
        imagePullPolicy: Never
        args: ["demo-server"]
        env:
        - {name: DEMO_MODE, value: "frontend"}
        - {name: DEMO_PORT, value: "8080"}
        - {name: DEMO_UPSTREAM, value: "http://backend.default.svc.cluster.local"}
---
apiVersion: v1
kind: Service
metadata:
  name: frontend
  namespace: default
spec:
  selector:
    app: frontend
  ports:
  - {port: 80, targetPort: 8080}
EOF
```

Wait for all pods to be ready:

```bash
kubectl wait --for=condition=available deployment/database-api deployment/backend deployment/frontend \
  -n default --timeout=60s
```

### 3. Verify normal traffic

From within the cluster, the full chain responds with data from the in-cluster database-api pod:

```
$ kubectl run curl-test --image=curlimages/curl:latest --restart=Never --rm -i \
    -- curl -s http://frontend.default.svc.cluster.local/

{"backend_data":{"processed":true,"service":"backend",
  "upstream_data":{"count":3,"inventory":["widget-A","widget-B","widget-C"],
    "service":"database-api","source":"cluster"}},
 "service":"frontend"}
```

`"source":"cluster"` — all three tiers serving from in-cluster pods.

### 4. Start a local replacement for database-api

```python
# /tmp/devserver.py
import http.server, json

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps({
            'service': 'database-api',
            'source': 'LOCAL_DEV',
            'inventory': ['new-item-1', 'new-item-2', 'NEW-FEATURE'],
            'count': 3,
            'dev_build': True
        }).encode())

http.server.HTTPServer(('', 9090), Handler).serve_forever()
```

```bash
python3 /tmp/devserver.py &
```

### 5. Intercept database-api

```
$ tether start

Starting kubectl port-forward to relay (localhost:31986)...

Available namespaces:
  1) default

Select namespace [1-1]: 1

Deployments in "default":
  1) backend
  2) database-api
  3) frontend

Select deployment [1-3]: 2

Found service database-api (targetPort: 8080)
Opening relay session...

Intercepting default/database-api → developer laptop

Share this command with the developer:

  tether connect --session eyJpZCI6Ijg0NGMyZjZhNmFjMDg1NTc0NmFhYjQwYTJjN2FhZmI1... --port <local-port>

To stop:  tether stop --session eyJpZCI6Ijg0NGMyZjZhNmFjMDg1NTc0NmFhYjQwYTJjN2FhZmI1...
```

### 6. Connect

```
$ tether connect --session eyJpZCI6Ijg0NGMyZjZhNmFjMDg1... --port 9090

Forwarding from 127.0.0.1:31986 -> 8080
Forwarding from [::1]:31986 -> 8080
Connected. Forwarding traffic to localhost:9090
Press Ctrl+C to disconnect.
```

### 7. Verify intercepted traffic

**Direct hit to database-api** — routed to the local dev server:

```
$ kubectl run curl-test --image=curlimages/curl:latest --restart=Never --rm -i \
    -- curl -s http://database-api.default.svc.cluster.local/

{"service":"database-api","source":"LOCAL_DEV",
 "inventory":["new-item-1","new-item-2","NEW-FEATURE"],
 "count":3,"dev_build":true}
```

**Full chain through frontend** — the local dev server is in the loop:

```
$ kubectl run curl-test --image=curlimages/curl:latest --restart=Never --rm -i \
    -- curl -s http://frontend.default.svc.cluster.local/

{"backend_data":{"processed":true,"service":"backend",
  "upstream_data":{"count":3,"dev_build":true,
    "inventory":["new-item-1","new-item-2","NEW-FEATURE"],
    "service":"database-api","source":"LOCAL_DEV"}},
 "service":"frontend"}
```

`"source":"LOCAL_DEV"` in `upstream_data` — the real cluster chain (frontend → backend) called through to the local machine, and the response flowed back through the full chain.

### 8. Stop and restore

```
$ tether stop --session eyJpZCI6Ijg0NGMyZjZhNmFjMDg1...

Restoring default/database-api...
Done. default/database-api is back to its original pods.
```

Traffic is immediately restored:

```
$ kubectl run curl-test --image=curlimages/curl:latest --restart=Never --rm -i \
    -- curl -s http://frontend.default.svc.cluster.local/

{"backend_data":{"processed":true,"service":"backend",
  "upstream_data":{"count":3,"inventory":["widget-A","widget-B","widget-C"],
    "service":"database-api","source":"cluster"}},
 "service":"frontend"}
```

`"source":"cluster"` — fully restored.

---

## Command reference

### `tether install`

```
tether install [--force]
```

Build and deploy tether into the current cluster. Must be run from the tether repository root (Dockerfile must be present).

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | false | Reinstall even if already installed; regenerates the PSK |

Supported cluster types and their detection rules:

| Cluster | Detection |
|---------|-----------|
| k3d | Context name starts with `k3d-` |
| minikube | Context name is `minikube` or starts with `minikube` |
| k3s | Server URL is `localhost` or `127.0.0.1`, and `k3s` binary is in PATH |

---

### `tether start`

```
tether start [deployment] [-n namespace]
```

Begin intercepting a deployment's service traffic. Prompts interactively for namespace and deployment if not provided.

| Flag | Default | Description |
|------|---------|-------------|
| `-n, --namespace` | (prompted) | Namespace of the deployment |

Session state is saved to `~/.tether/sessions/<id>.json` for crash recovery.

---

### `tether connect`

```
tether connect --session <token> -p <local-port>
```

Forward intercepted cluster traffic to a local port. Runs until Ctrl+C or until ops calls `tether stop`.

| Flag | Default | Description |
|------|---------|-------------|
| `--session` | (required) | Session token printed by `tether start` |
| `-p, --port` | (required) | Local port to forward traffic to |

---

### `tether stop`

```
tether stop --session <token>
```

Stop intercepting and restore the original service. Works even if `tether connect` was never run.

| Flag | Default | Description |
|------|---------|-------------|
| `--session` | (required) | Session token printed by `tether start` |

---

### `tether server`

```
tether server [--addr <address>]
```

Run the relay server. Deployed automatically in-cluster by `tether install`. Reads `RELAY_PSK` from the environment.

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:8080` | TCP address to listen on |

---

### Global flags

These flags are available on every command:

| Flag | Default | Description |
|------|---------|-------------|
| `--kubeconfig` | `$KUBECONFIG` or `~/.kube/config` | Path to kubeconfig file |
| `--context` | Current context | Kubernetes context to use |

---

## Multiple concurrent sessions

Multiple services can be intercepted at the same time. Each session is independent — its own ID, its own port listener on the agent pod, its own token:

```bash
# Terminal 1 — ops intercepts database-api
tether start -n default database-api
# → share token-A with developer A

# Terminal 2 — ops intercepts backend
tether start -n default backend
# → share token-B with developer B

# Stop them independently
tether stop --session <token-A>
tether stop --session <token-B>
```

Two sessions cannot intercept the same service simultaneously.

---

## Troubleshooting

### `tether install` fails: "unrecognised cluster type"

tether could not determine the cluster type from your kubectl context. Check that:
- For k3d: your context name starts with `k3d-` (this is k3d's default naming)
- For minikube: your context is named `minikube`
- For k3s: the `k3s` binary is in your PATH and the server URL in your kubeconfig is `https://localhost:6443` or `https://127.0.0.1:6443`

To check your current context:
```bash
kubectl config current-context
kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'
```

### `tether start` fails: "tether not installed — run 'tether install' first"

The `tether-config` ConfigMap does not exist in the `tether` namespace. Run `tether install` from the tether repository root.

### `tether start` fails: "no service found matching deployment X"

The deployment exists but no Service in the same namespace has a selector that matches the deployment's pod labels. Verify that your Service's `spec.selector` matches the labels in your deployment's `spec.template.metadata.labels`:

```bash
kubectl get deployment <name> -o jsonpath='{.spec.selector.matchLabels}'
kubectl get services -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.selector}{"\n"}{end}'
```

### `tether start` fails: "no running agent pod found"

The `tether-agent` pod is not running. Check its status:
```bash
kubectl get pods -n tether
kubectl describe pod -n tether -l app=tether-agent
```

If the pod is in `CrashLoopBackOff`, check the logs:
```bash
kubectl logs -n tether -l app=tether-agent
```

The most common cause is that the image was not imported correctly. Rerun `tether install --force`.

### `tether connect` disconnects immediately

The session may have already been closed by `tether stop`, or the relay pod restarted. Check the relay pod is running:
```bash
kubectl get pods -n tether
```

If the relay restarted, the active session was lost. Run `tether stop --session <token>` to restore the service, then start a new session with `tether start`.

### Service not fully restored after `tether stop`

`tether stop` restores the service selector immediately. If traffic still appears to be intercepted a few seconds after stop, kube-proxy may still be propagating the endpoint change. This typically resolves within 2–5 seconds.

If the service remains broken (no traffic at all), check whether the Endpoints object was correctly restored:
```bash
kubectl get endpoints <service-name> -n <namespace>
```

The endpoint should list the original pod IP(s), not the agent pod IP (`kubectl get pod -n tether -l app=tether-agent -o wide` shows the agent IP).
