#!/usr/bin/env bash
set -euo pipefail

TETHER_NAMESPACE="tether"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── colours ────────────────────────────────────────────────────────────────────
BOLD=$(tput bold 2>/dev/null || true)
GREEN=$(tput setaf 2 2>/dev/null || true)
CYAN=$(tput setaf 6 2>/dev/null || true)
YELLOW=$(tput setaf 3 2>/dev/null || true)
RED=$(tput setaf 1 2>/dev/null || true)
DIM=$(tput dim 2>/dev/null || true)
RESET=$(tput sgr0 2>/dev/null || true)

die()  { echo ""; echo "${RED}  ✗ $*${RESET}" >&2; exit 1; }
info() { echo "${CYAN}  ▸ $*${RESET}"; }
ok()   { echo "${GREEN}  ✓ $*${RESET}"; }
warn() { echo "${YELLOW}  ! $*${RESET}"; }
step() { echo ""; echo "${BOLD}── $* ${DIM}$(printf '─%.0s' $(seq 1 $((50 - ${#1}))))${RESET}"; echo ""; }
prompt() { printf "${BOLD}  $1${RESET}"; }

# ── SSH helpers ────────────────────────────────────────────────────────────────
SSH_PASS=""
SSH_USER=""
K3S_HOST=""

ssh_run() {
  if [[ -n "$SSH_PASS" ]]; then
    sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 "${SSH_USER}@${K3S_HOST}" "$@"
  else
    ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 "${SSH_USER}@${K3S_HOST}" "$@"
  fi
}

scp_to() {
  local src="$1" dst="$2"
  if [[ -n "$SSH_PASS" ]]; then
    sshpass -p "$SSH_PASS" scp -o StrictHostKeyChecking=no "$src" "${SSH_USER}@${K3S_HOST}:${dst}"
  else
    scp -o StrictHostKeyChecking=no "$src" "${SSH_USER}@${K3S_HOST}:${dst}"
  fi
}

# ── NodePort scanner ───────────────────────────────────────────────────────────
find_free_nodeport() {
  local used
  used=$(kubectl get svc --all-namespaces \
    -o jsonpath='{range .items[*]}{range .spec.ports[*]}{.nodePort}{"\n"}{end}{end}' 2>/dev/null \
    | grep -v '^$' | sort -n)
  for port in $(seq 31001 32767); do
    if ! echo "$used" | grep -qx "$port"; then
      echo "$port"
      return
    fi
  done
  die "No free NodePort available in range 31001-32767"
}

nodeport_in_use() {
  kubectl get svc --all-namespaces \
    -o jsonpath='{range .items[*]}{range .spec.ports[*]}{.nodePort}{"\n"}{end}{end}' 2>/dev/null \
    | grep -qx "$1"
}

# ── detect local LAN IP ────────────────────────────────────────────────────────
detect_local_ip() {
  ip route get 8.8.8.8 2>/dev/null | grep -oP 'src \K\S+' 2>/dev/null \
    || hostname -I 2>/dev/null | awk '{print $1}' \
    || echo ""
}

# ══════════════════════════════════════════════════════════════════════════════
echo ""
echo "${BOLD}${CYAN}  tether — cluster installer${RESET}"
echo ""

# ── STEP 1: Preflight ─────────────────────────────────────────────────────────
step "Preflight checks"

command -v kubectl >/dev/null 2>&1 || die "kubectl not found — install it and retry"
command -v docker  >/dev/null 2>&1 || die "docker not found — install it and retry"
[[ -f "$REPO_ROOT/Dockerfile" ]] || die "Dockerfile not found — run this script from inside the tether repo"

KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/config}"
[[ -f "$KUBECONFIG_PATH" ]] || die "kubeconfig not found at ${KUBECONFIG_PATH} — set KUBECONFIG or create ~/.kube/config"
export KUBECONFIG="$KUBECONFIG_PATH"

kubectl cluster-info >/dev/null 2>&1 || die "Cannot reach cluster — check your kubeconfig"
ok "kubectl connected"
ok "docker available"
ok "kubeconfig: ${KUBECONFIG_PATH}"

# ── STEP 2: Detect cluster type ───────────────────────────────────────────────
step "Detecting cluster type"

CURRENT_CONTEXT=$(kubectl config current-context 2>/dev/null || echo "unknown")
SERVER_URL=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null || echo "")
SERVER_HOST=$(echo "$SERVER_URL" | sed 's|https://||;s|http://||' | cut -d: -f1)

CLUSTER_TYPE=""
K3D_CLUSTER_NAME=""

if [[ "$CURRENT_CONTEXT" == k3d-* ]]; then
  CLUSTER_TYPE="k3d"
  K3D_CLUSTER_NAME="${CURRENT_CONTEXT#k3d-}"
  info "k3d cluster detected: ${K3D_CLUSTER_NAME}"
elif [[ "$SERVER_HOST" == "127.0.0.1" || "$SERVER_HOST" == "localhost" ]]; then
  if command -v k3s >/dev/null 2>&1; then
    CLUSTER_TYPE="k3s-local"
    info "k3s (local) detected"
  elif command -v k3d >/dev/null 2>&1; then
    # Local server but not k3s — assume k3d with non-standard context name
    CLUSTER_TYPE="k3d"
    K3D_CLUSTER_NAME=$(k3d cluster list -o json 2>/dev/null | python3 -c "import json,sys; clusters=json.load(sys.stdin); print(clusters[0]['name'])" 2>/dev/null || echo "$CURRENT_CONTEXT")
    warn "Local cluster — assuming k3d (cluster: ${K3D_CLUSTER_NAME})"
  else
    die "Local cluster detected but neither k3s nor k3d found — cannot determine import method"
  fi
else
  CLUSTER_TYPE="k3s-remote"
  K3S_HOST="$SERVER_HOST"
  info "Remote k3s cluster detected at ${K3S_HOST}"
fi

ok "Cluster type: ${CLUSTER_TYPE}"

# ── STEP 3: SSH setup (k3s-remote only) ───────────────────────────────────────
if [[ "$CLUSTER_TYPE" == "k3s-remote" ]]; then
  step "SSH access for image import"
  info "Direct image import requires SSH into ${K3S_HOST}"

  prompt "SSH username [$(whoami)]: "
  read -r SSH_USER
  SSH_USER="${SSH_USER:-$(whoami)}"

  # Try key-based auth first
  if ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=no \
       "${SSH_USER}@${K3S_HOST}" true 2>/dev/null; then
    ok "SSH key auth succeeded"
  else
    command -v sshpass >/dev/null 2>&1 || \
      die "SSH key auth failed and sshpass is not installed — set up SSH key auth or install sshpass"
    prompt "SSH password for ${SSH_USER}@${K3S_HOST}: "
    read -rs SSH_PASS
    echo ""
    sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 \
      "${SSH_USER}@${K3S_HOST}" true 2>/dev/null || die "SSH authentication failed"
    ok "SSH password auth succeeded"
  fi
fi

# ── STEP 4: Relay location ────────────────────────────────────────────────────
step "Relay configuration"

echo "  Where should the relay run?"
echo ""
echo "  ${CYAN}1)${RESET} ${BOLD}In the cluster${RESET}  — Deployment + NodePort in '${TETHER_NAMESPACE}' namespace"
echo "     ${DIM}Best for shared dev servers (k3s). Relay is always-on, no local process needed.${RESET}"
echo ""
echo "  ${CYAN}2)${RESET} ${BOLD}Local machine${RESET}   — Run 'tether server' on this machine"
echo "     ${DIM}Best for k3d / personal dev clusters. Relay runs as a local process.${RESET}"
echo ""
prompt "Select [1/2]: "
read -r RELAY_CHOICE
echo ""

case "$RELAY_CHOICE" in
  1) RELAY_MODE="in-cluster" ;;
  2) RELAY_MODE="local" ;;
  *) die "Invalid choice — enter 1 or 2" ;;
esac

# ── in-cluster: NodePort selection ────────────────────────────────────────────
RELAY_NODE_PORT=""
if [[ "$RELAY_MODE" == "in-cluster" ]]; then
  info "Scanning for free NodePorts..."
  SUGGESTED_PORT=$(find_free_nodeport)
  ok "First available NodePort: ${SUGGESTED_PORT}"
  echo ""
  prompt "NodePort to use [${SUGGESTED_PORT}]: "
  read -r PORT_INPUT
  RELAY_NODE_PORT="${PORT_INPUT:-$SUGGESTED_PORT}"

  if nodeport_in_use "$RELAY_NODE_PORT"; then
    die "NodePort ${RELAY_NODE_PORT} is already in use — choose another"
  fi
  ok "Relay NodePort: ${RELAY_NODE_PORT}"
fi

# ── local relay: port + address confirmation ───────────────────────────────────
LOCAL_RELAY_PORT=""
RELAY_ADDR_FOR_AGENT=""  # address pods use to reach relay
RELAY_ADDR_FOR_OPS=""    # address ops (tether start) and devs use

if [[ "$RELAY_MODE" == "local" ]]; then
  prompt "Relay port [8080]: "
  read -r LOCAL_RELAY_PORT
  LOCAL_RELAY_PORT="${LOCAL_RELAY_PORT:-8080}"

  echo ""
  if [[ "$CLUSTER_TYPE" == "k3d" ]]; then
    # k3d pods reach the host via host.docker.internal
    RELAY_ADDR_FOR_AGENT="host.docker.internal:${LOCAL_RELAY_PORT}"
    info "k3d detected — agent will connect to relay via: ${RELAY_ADDR_FOR_AGENT}"

    # Ops and developers use the actual LAN IP
    DETECTED_IP=$(detect_local_ip)
    echo ""
    info "Detected local IP for ops/developer access: ${BOLD}${DETECTED_IP}${RESET}"
    prompt "Confirm relay address for ops and developers [${DETECTED_IP}:${LOCAL_RELAY_PORT}]: "
    read -r OPS_RELAY_INPUT
    RELAY_ADDR_FOR_OPS="${OPS_RELAY_INPUT:-${DETECTED_IP}:${LOCAL_RELAY_PORT}}"
  else
    # k3s-local or k3s-remote: agent uses the same IP as ops/devs
    DETECTED_IP=$(detect_local_ip)
    echo ""
    info "Detected local IP: ${BOLD}${DETECTED_IP}${RESET}"
    prompt "Confirm relay address (used by agent, ops, and developers) [${DETECTED_IP}:${LOCAL_RELAY_PORT}]: "
    read -r RELAY_INPUT
    RELAY_ADDR_FOR_AGENT="${RELAY_INPUT:-${DETECTED_IP}:${LOCAL_RELAY_PORT}}"
    RELAY_ADDR_FOR_OPS="$RELAY_ADDR_FOR_AGENT"
  fi

  ok "Agent relay address: ${RELAY_ADDR_FOR_AGENT}"
  ok "Ops/developer relay address: ${RELAY_ADDR_FOR_OPS}"
fi

# ── STEP 5: Build image ───────────────────────────────────────────────────────
step "Building tether image"

cd "$REPO_ROOT"
info "Running docker build..."
docker build --platform linux/amd64 -t tether:latest . 2>&1 \
  | grep -E '^(#[0-9]+ (DONE|ERROR)|Successfully)' || true
ok "Image built: tether:latest"

# ── STEP 6: Import image into cluster ────────────────────────────────────────
step "Importing image into cluster"

TMP_TAR=$(mktemp /tmp/tether-XXXXXX.tar.gz)
info "Saving image..."
docker save tether:latest | gzip > "$TMP_TAR"
info "Saved ($(du -sh "$TMP_TAR" | cut -f1))"

case "$CLUSTER_TYPE" in
  k3d)
    command -v k3d >/dev/null 2>&1 || die "k3d CLI not found — install it from https://k3d.io"
    info "Importing into k3d cluster '${K3D_CLUSTER_NAME}'..."
    k3d image import "$TMP_TAR" -c "$K3D_CLUSTER_NAME" 2>&1 | grep -v '^$' || true
    ok "Image imported into k3d cluster: ${K3D_CLUSTER_NAME}"
    ;;
  k3s-local)
    info "Importing into local k3s..."
    sudo k3s ctr images import "$TMP_TAR" 2>&1 | tail -2
    ok "Image imported into local k3s"
    ;;
  k3s-remote)
    info "Copying image to ${K3S_HOST}..."
    scp_to "$TMP_TAR" "/tmp/tether-import.tar.gz"
    info "Importing on remote node..."
    ssh_run "sudo k3s ctr images import /tmp/tether-import.tar.gz && rm /tmp/tether-import.tar.gz"
    ok "Image imported into remote k3s at ${K3S_HOST}"
    ;;
esac

rm -f "$TMP_TAR"

# ── STEP 7: Deploy to cluster ─────────────────────────────────────────────────
step "Deploying to cluster"

# Create namespace (idempotent)
kubectl create namespace "$TETHER_NAMESPACE" --dry-run=client -o yaml \
  | kubectl apply -f - >/dev/null
ok "Namespace '${TETHER_NAMESPACE}' ready"

# Deploy relay (in-cluster mode only)
if [[ "$RELAY_MODE" == "in-cluster" ]]; then
  RELAY_ADDR_FOR_AGENT="tether-relay.${TETHER_NAMESPACE}.svc.cluster.local:8080"

  kubectl apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tether-relay
  namespace: ${TETHER_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: tether-relay
  template:
    metadata:
      labels:
        app: tether-relay
    spec:
      containers:
        - name: relay
          image: docker.io/library/tether:latest
          imagePullPolicy: Never
          args: ["server", "--addr", ":8080"]
          ports:
            - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: tether-relay
  namespace: ${TETHER_NAMESPACE}
spec:
  type: NodePort
  selector:
    app: tether-relay
  ports:
    - name: relay
      port: 8080
      targetPort: 8080
      nodePort: ${RELAY_NODE_PORT}
EOF
  ok "Relay deployment + NodePort service applied"
fi

# Deploy agent (always)
kubectl apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tether-agent
  namespace: ${TETHER_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: tether-agent
  template:
    metadata:
      labels:
        app: tether-agent
    spec:
      containers:
        - name: agent
          image: docker.io/library/tether:latest
          imagePullPolicy: Never
          args: ["agent"]
          env:
            - name: RELAY_ADDR
              value: "${RELAY_ADDR_FOR_AGENT}"
EOF
ok "Agent deployment applied"

# ── STEP 8: Wait for pods ─────────────────────────────────────────────────────
step "Waiting for pods"

if [[ "$RELAY_MODE" == "in-cluster" ]]; then
  kubectl rollout status deployment/tether-relay -n "$TETHER_NAMESPACE" --timeout=120s
  ok "tether-relay is running"
fi
kubectl rollout status deployment/tether-agent -n "$TETHER_NAMESPACE" --timeout=120s
ok "tether-agent is running"

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "${BOLD}${GREEN}  Tether installed successfully!${RESET}"
echo ""

if [[ "$RELAY_MODE" == "in-cluster" ]]; then
  NODE_IP=$(kubectl get nodes \
    -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null \
    || echo "<node-ip>")
  RELAY_EXTERNAL="${NODE_IP}:${RELAY_NODE_PORT}"

  echo "  ${BOLD}Relay:${RESET} ${YELLOW}${RELAY_EXTERNAL}${RESET} (NodePort — reachable from all developers)"
  echo ""
  echo "  ${BOLD}Intercept a service:${RESET}"
  echo "  ${CYAN}tether start <deployment> -n <namespace> --relay ${RELAY_EXTERNAL}${RESET}"
else
  echo "  ${BOLD}First, start the relay on this machine:${RESET}"
  echo "  ${CYAN}tether server --addr :${LOCAL_RELAY_PORT}${RESET}"
  echo ""
  echo "  ${BOLD}Then intercept a service:${RESET}"
  echo "  ${CYAN}tether start <deployment> -n <namespace> --relay ${RELAY_ADDR_FOR_OPS}${RESET}"
fi

echo ""
echo "  ${BOLD}Developer install (one-liner):${RESET}"
echo "  ${CYAN}curl -fsSL https://raw.githubusercontent.com/1mr0-tech/tether/main/scripts/install-tether.sh | bash${RESET}"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
