#!/bin/bash

set -e

# Variables
NAMESPACE="vault"


PODS=($(kubectl get pods -n $NAMESPACE -l app.kubernetes.io/name=vault -o jsonpath="{.items[*].metadata.name}"))

echo "Waiting for Vault pods to be ready..."
for ((i=1; i<${#PODS[@]}; i++)); do
  FOLLOWER="${PODS[$i]}"
  kubectl wait --for=condition=ready pod $FOLLOWER -n $NAMESPACE --timeout=30s
done

ROOT_TOKEN=$(cat vault-init.json | jq -r '.root_token')

# Login and verify
kubectl exec -n $NAMESPACE "${PODS[0]}" -- vault login "$ROOT_TOKEN"
kubectl exec -n $NAMESPACE "${PODS[0]}" -- vault operator raft list-peers
