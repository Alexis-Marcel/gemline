#!/usr/bin/env bash
# Install ArgoCD on the cluster, then declare the Gemline application
# so it self-syncs from the repo's deploy/k8s/ directory.
#
# Run once, from your laptop with KUBECONFIG pointed at the cluster.
# Idempotent: re-running it picks up new ArgoCD releases or Application
# spec changes.

set -euo pipefail

ARGOCD_VERSION="v2.12.4"

echo "==> installing ArgoCD ${ARGOCD_VERSION}"
kubectl apply -k "github.com/argoproj/argo-cd//manifests/cluster-install?ref=${ARGOCD_VERSION}"

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
