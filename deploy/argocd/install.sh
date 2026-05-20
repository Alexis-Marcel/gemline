#!/usr/bin/env bash
# Install ArgoCD on the cluster, then declare the Gemline + monitoring
# Applications so they self-sync from the repo.
#
# ⚠️  FRESH CLUSTERS DON'T NEED THIS SCRIPT.
# The control-plane cloud-init
# (deploy/terraform/cloud-init/control-plane.sh.tpl) already installs
# ArgoCD and applies both Applications at first boot. Use this script
# only to:
#   - Re-bootstrap ArgoCD on a cluster where it was removed
#   - Upgrade ArgoCD on an existing cluster (bump ARGOCD_VERSION below)
#   - Apply the Applications on a cluster that was provisioned by some
#     other means than this repo's Terraform
#
# Run from your laptop with KUBECONFIG pointed at the cluster.
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
#
# --server-side is required: the applicationsets.argoproj.io CRD carries
# an OpenAPI schema larger than the 256KB limit on the
# last-applied-configuration annotation that client-side apply uses.
# --force-conflicts lets us take ownership of fields previously managed
# by an older ArgoCD install.
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"

echo "==> waiting for ArgoCD components"
for d in argocd-server argocd-repo-server argocd-applicationset-controller argocd-notifications-controller; do
  kubectl -n argocd rollout status deployment/$d --timeout=3m
done
kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=3m

echo "==> applying the sealed-secrets Application"
# Apply first so the controller is up when the gemline kustomize
# carries a SealedSecret. The CRD is what the SealedSecret manifest
# references; ArgoCD's gemline sync waits/retries until it exists.
kubectl apply -f "$(dirname "$0")/app-sealed-secrets.yaml"

echo "==> applying the external-secrets Application"
# ESO + Infisical for cluster secrets (DSN, etc.). Apply before gemline
# so the ExternalSecret CRD exists when the gemline kustomize tries to
# create ExternalSecret resources.
kubectl apply -f "$(dirname "$0")/app-external-secrets.yaml"

echo "==> applying the eso-config Application"
# Cross-namespace ESO resources (Infisical auth SealedSecret,
# ClusterSecretStore, ExternalSecrets for shared services).
kubectl apply -f "$(dirname "$0")/app-eso-config.yaml"

echo "==> applying the monitoring Application (kube-prometheus-stack)"
# Apply this BEFORE app-gemline so the ServiceMonitor CRD exists when
# ArgoCD syncs the gemline kustomize (which includes a ServiceMonitor).
# We don't block on the sync completing here — auto-sync will pick it
# up, and the gemline ServiceMonitor carries SkipDryRunOnMissingResource
# so it'll retry until the CRD is registered.
kubectl apply -f "$(dirname "$0")/app-monitoring.yaml"

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

To reach Grafana (after the monitoring app finishes its first sync):
  kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
  open http://localhost:3000

Login as 'admin' / 'changeme' (the value set in app-monitoring.yaml).
Rotate the password from the Grafana UI on first login.
EOF
