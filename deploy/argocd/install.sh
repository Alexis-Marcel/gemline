#!/usr/bin/env bash
# Install ArgoCD and declare the app-of-apps so they self-sync from the repo.
#
# Fresh clusters don't need this: `make deploy` runs Ansible, which already
# installs ArgoCD and applies the Applications. Use this script only to
# re-bootstrap or upgrade ArgoCD on an existing cluster, from your laptop
# with KUBECONFIG set. Idempotent.

set -euo pipefail

# v3 required for k3s 1.31+: v2's Deployment schema lacks
# .status.terminatingReplicas, so diff calc fails and sync status oscillates.
ARGOCD_VERSION="v3.4.1"

echo "==> creating argocd namespace"
kubectl get namespace argocd >/dev/null 2>&1 || kubectl create namespace argocd

echo "==> installing ArgoCD ${ARGOCD_VERSION}"
# --server-side: the applicationsets CRD's OpenAPI schema exceeds the 256KB
# client-side-apply annotation limit. --force-conflicts takes ownership of
# fields managed by an older ArgoCD install.
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"

echo "==> waiting for ArgoCD components"
for d in argocd-server argocd-repo-server argocd-applicationset-controller argocd-notifications-controller; do
  kubectl -n argocd rollout status deployment/$d --timeout=3m
done
kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=3m

echo "==> applying the sealed-secrets Application"
# Before gemline so its SealedSecret CRD exists when the gemline sync runs.
kubectl apply -f "$(dirname "$0")/app-sealed-secrets.yaml"

echo "==> applying the external-secrets Application"
# Before gemline so the ExternalSecret CRD exists for the gemline kustomize.
kubectl apply -f "$(dirname "$0")/app-external-secrets.yaml"

echo "==> applying the eso-config Application"
kubectl apply -f "$(dirname "$0")/app-eso-config.yaml"

echo "==> applying the monitoring Application (kube-prometheus-stack)"
# Before gemline so the ServiceMonitor CRD exists when ArgoCD syncs gemline.
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
