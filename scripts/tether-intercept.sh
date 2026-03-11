#!/usr/bin/env bash
set -euo pipefail

RELAY="10.1.3.23:31800"
KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/config}"

# ── colours ────────────────────────────────────────────────────────────────────
BOLD=$(tput bold 2>/dev/null || true)
GREEN=$(tput setaf 2 2>/dev/null || true)
CYAN=$(tput setaf 6 2>/dev/null || true)
YELLOW=$(tput setaf 3 2>/dev/null || true)
RED=$(tput setaf 1 2>/dev/null || true)
RESET=$(tput sgr0 2>/dev/null || true)

die() { echo "${RED}error: $*${RESET}" >&2; exit 1; }

# ── preflight checks ───────────────────────────────────────────────────────────
command -v tether >/dev/null 2>&1 || die "'tether' binary not found in PATH"
command -v kubectl >/dev/null 2>&1 || die "'kubectl' not found in PATH"
[[ -f "$KUBECONFIG_PATH" ]] || die "kubeconfig not found at $KUBECONFIG_PATH"

export KUBECONFIG="$KUBECONFIG_PATH"

echo ""
echo "${BOLD}${CYAN}  tether — intercept a cluster service${RESET}"
echo "  relay: ${YELLOW}${RELAY}${RESET}"
echo ""

# ── pick namespace ─────────────────────────────────────────────────────────────
echo "${BOLD}Available namespaces:${RESET}"
mapfile -t NAMESPACES < <(kubectl get namespaces -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep -v -E '^(kube-system|kube-public|kube-node-lease|tether)$' | sort)

for i in "${!NAMESPACES[@]}"; do
  printf "  ${CYAN}%2d)${RESET} %s\n" "$((i+1))" "${NAMESPACES[$i]}"
done
echo ""

while true; do
  read -rp "${BOLD}Select namespace [number or name]: ${RESET}" NS_INPUT
  if [[ "$NS_INPUT" =~ ^[0-9]+$ ]] && (( NS_INPUT >= 1 && NS_INPUT <= ${#NAMESPACES[@]} )); then
    NAMESPACE="${NAMESPACES[$((NS_INPUT-1))]}"
    break
  elif printf '%s\n' "${NAMESPACES[@]}" | grep -qx "$NS_INPUT"; then
    NAMESPACE="$NS_INPUT"
    break
  else
    echo "${YELLOW}  Not a valid selection, try again.${RESET}"
  fi
done

echo ""

# ── pick deployment ────────────────────────────────────────────────────────────
echo "${BOLD}Deployments in '${NAMESPACE}':${RESET}"
mapfile -t DEPLOYMENTS < <(kubectl get deployments -n "$NAMESPACE" -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | sort)

[[ ${#DEPLOYMENTS[@]} -eq 0 ]] && die "No deployments found in namespace '$NAMESPACE'"

for i in "${!DEPLOYMENTS[@]}"; do
  printf "  ${CYAN}%2d)${RESET} %s\n" "$((i+1))" "${DEPLOYMENTS[$i]}"
done
echo ""

while true; do
  read -rp "${BOLD}Select deployment [number or name]: ${RESET}" DEP_INPUT
  if [[ "$DEP_INPUT" =~ ^[0-9]+$ ]] && (( DEP_INPUT >= 1 && DEP_INPUT <= ${#DEPLOYMENTS[@]} )); then
    DEPLOYMENT="${DEPLOYMENTS[$((DEP_INPUT-1))]}"
    break
  elif printf '%s\n' "${DEPLOYMENTS[@]}" | grep -qx "$DEP_INPUT"; then
    DEPLOYMENT="$DEP_INPUT"
    break
  else
    echo "${YELLOW}  Not a valid selection, try again.${RESET}"
  fi
done

echo ""
echo "${BOLD}Starting interception: ${CYAN}${NAMESPACE}/${DEPLOYMENT}${RESET}"
echo ""

# ── run tether start and capture output ───────────────────────────────────────
TMPOUT=$(mktemp)
tether start "$DEPLOYMENT" \
  -n "$NAMESPACE" \
  --relay "$RELAY" \
  --kubeconfig "$KUBECONFIG_PATH" \
  2>&1 | tee "$TMPOUT"

echo ""

# ── extract and highlight the connect command ──────────────────────────────────
CONNECT_CMD=$(grep -o 'tether connect --session [^ ]* --port <local-port>' "$TMPOUT" || true)
STOP_CMD=$(grep -o 'tether stop --session [^ ]*' "$TMPOUT" || true)
rm -f "$TMPOUT"

if [[ -z "$CONNECT_CMD" ]]; then
  die "Could not find connect command in tether output — check errors above"
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "${BOLD}${GREEN}  Send this to the developer:${RESET}"
echo ""
echo "  ${BOLD}${YELLOW}${CONNECT_CMD}${RESET}"
echo ""
echo "  Replace ${CYAN}<local-port>${RESET} with the port their app will listen on locally."
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "${BOLD}To stop and restore the service:${RESET}"
echo ""
echo "  ${STOP_CMD}"
echo ""

# ── copy to clipboard if possible ─────────────────────────────────────────────
if command -v xclip >/dev/null 2>&1; then
  echo "$CONNECT_CMD" | xclip -selection clipboard
  echo "${CYAN}  (connect command copied to clipboard)${RESET}"
elif command -v xsel >/dev/null 2>&1; then
  echo "$CONNECT_CMD" | xsel --clipboard --input
  echo "${CYAN}  (connect command copied to clipboard)${RESET}"
elif command -v pbcopy >/dev/null 2>&1; then
  echo "$CONNECT_CMD" | pbcopy
  echo "${CYAN}  (connect command copied to clipboard)${RESET}"
fi

echo ""
