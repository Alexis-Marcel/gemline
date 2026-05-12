#!/usr/bin/env bash
# Install ArgoCD on the cluster, then declare the Gemline application
# so it self-syncs from the repo's deploy/k8s/ directory.
#
# Run once, from your laptop with KUBECONFIG pointed at the cluster.
# Idempotent: re-running it picks up new ArgoCD releases or Application
# spec changes.

set -euo pipefail

# v3 is required for k3s 1.31+: v2 ships an older Deployment schema that
# doesn't know about .status.terminatingReplicas, so diff calculation
# fails ("field not declared in schema") and sync status oscillates as
# Unknown.
ARGOCD_VERSION="v3.4.1"

echo "==> creating argocd namespace"
kubectl get namespace argocd >/dev/null 2>&1 || kubectl create namespace argocd

echo "==> installing ArgoCD ${ARGOCD_VERSION}"
# We deliberately use the flat install.yaml (not the kustomize bundle at
# manifests/cluster-install) because the latter silently falls back to
# kubectl's current namespace if `argocd` doesn't exist yet — landing
# the install in `default`. The flat manifest has the namespace baked
# in and combined with `-n argocd` is unambiguous.
kubectl apply -n argocd \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"

echo "==> waiting for ArgoCD components"
for d in argocd-server argocd-repo-server argocd-applicationset-controller argocd-notifications-controller; do
  kubectl -n argocd rollout status deployment/$d --timeout=3m
done
kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=3m

echo "==> applying the Gemline Application manifest"
kubectl apply -f "$(dirname "$0")/app-gemline.yaml"

cat <<EOF

==> done

ArgoCD is up and watching deploy/k8s/ on the main branch with
auto-sync. Any commit there will be applied to the cluster
automatically.

To reach the UI:
  kubectl -n argocd port-forward svc/argocd-server 8443:443
  open https://localhost:8443

Login as 'admin' — the initial password is in the secret:
  kubectl -n argocd get secret argocd-initial-admin-secret \\
    -o jsonpath='{.data.password}' | base64 -d; echo

Change it as soon as you've logged in (User Info → Update Password).
EOF
