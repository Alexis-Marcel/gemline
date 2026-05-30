# Single entrypoint for the provision + configure flow. Run from deploy/.
#   make deploy   terraform apply + ansible-playbook (full bootstrap)
#   make help     list targets

TERRAFORM_DIR := terraform
ANSIBLE_DIR   := ansible
KUBECONFIG_LOCAL := $(HOME)/.kube/gemline.yaml

.PHONY: help deploy plan ansible ansible-check ssh-cp ssh-w1 kubeconfig publish-jwks destroy
.DEFAULT_GOAL := help

help:
	@echo "Available targets:"
	@echo "  deploy          terraform apply + ansible-playbook (full bootstrap)"
	@echo "  plan            terraform plan (preview infra changes)"
	@echo "  ansible         run the Ansible playbook (cluster must exist)"
	@echo "  ansible-check   Ansible dry-run (--check)"
	@echo "  ssh-cp          SSH to the control plane"
	@echo "  ssh-w1          SSH to the first worker"
	@echo "  kubeconfig      fetch kubeconfig to $(KUBECONFIG_LOCAL)"
	@echo "  publish-jwks    fetch cluster OIDC discovery+JWKS, upload to S3 (needed once after each install)"
	@echo "  destroy         tear down the cluster (with confirmation)"

deploy: tf-apply ansible
	@echo ""
	@echo "==> Bootstrap complete!"
	@echo "    App:     https://gemline.werilo.fr"
	@echo "    ArgoCD:  kubectl -n argocd port-forward svc/argocd-server 8443:443"
	@echo "    Grafana: kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80"

tf-apply:
	cd $(TERRAFORM_DIR) && terraform apply

plan:
	cd $(TERRAFORM_DIR) && terraform plan

# --ask-vault-pass prompts for the vault password; export
# ANSIBLE_VAULT_PASSWORD_FILE to skip the prompt.
ansible:
	cd $(ANSIBLE_DIR) && ansible-playbook playbook.yaml --ask-vault-pass

ansible-check:
	cd $(ANSIBLE_DIR) && ansible-playbook playbook.yaml --check --ask-vault-pass

ssh-cp:
	ssh root@$$(cd $(TERRAFORM_DIR) && terraform output -raw control_plane_ipv4)

ssh-w1:
	ssh root@$$(cd $(TERRAFORM_DIR) && terraform output -json worker_ipv4 | jq -r '.[0]')

# Fetch the kubeconfig from cp1 and rewrite the loopback address to the
# Load Balancer's public IP, so kubectl survives any single CP failure.
kubeconfig:
	@CP_IP=$$(cd $(TERRAFORM_DIR) && terraform output -raw control_plane_ipv4); \
	LB_IP=$$(cd $(TERRAFORM_DIR) && terraform output -raw load_balancer_ipv4); \
	ssh root@$$CP_IP cat /etc/rancher/k3s/k3s.yaml \
	  | sed "s/127.0.0.1/$$LB_IP/" > $(KUBECONFIG_LOCAL); \
	chmod 600 $(KUBECONFIG_LOCAL); \
	echo "Wrote kubeconfig to $(KUBECONFIG_LOCAL) (server: $$LB_IP via the LB)"; \
	echo "Run:  export KUBECONFIG=$(KUBECONFIG_LOCAL)"

# Fetch the cluster's OIDC discovery + JWKS, upload to S3 so AWS STS can
# validate pod tokens (IRSA-equivalent). Run once after each fresh install;
# rerun if k3s rotates its service-account signing keys.
# Requires KUBECONFIG to point at the cluster (run `make kubeconfig` first).
publish-jwks:
	@BUCKET=gemline-cluster-oidc-386324384913; \
	REGION=eu-west-3; \
	set -e; \
	echo "Fetching OIDC discovery + JWKS via local kubectl..."; \
	kubectl get --raw /.well-known/openid-configuration > /tmp/oidc-discovery.json; \
	kubectl get --raw /openid/v1/jwks > /tmp/oidc-jwks.json; \
	test -s /tmp/oidc-discovery.json || { echo "ERROR: discovery doc empty — is kubectl pointing at the right cluster?"; exit 1; }; \
	test -s /tmp/oidc-jwks.json || { echo "ERROR: jwks empty"; exit 1; }; \
	echo "  discovery: $$(wc -c < /tmp/oidc-discovery.json) bytes"; \
	echo "  jwks:      $$(wc -c < /tmp/oidc-jwks.json) bytes"; \
	echo "Uploading to s3://$$BUCKET/..."; \
	aws s3 cp /tmp/oidc-discovery.json s3://$$BUCKET/.well-known/openid-configuration --content-type application/json --region $$REGION; \
	aws s3 cp /tmp/oidc-jwks.json s3://$$BUCKET/openid/v1/jwks --content-type application/json --region $$REGION; \
	echo "Done. Verify: curl https://$$BUCKET.s3.$$REGION.amazonaws.com/.well-known/openid-configuration"

# No --auto-approve: destroying a cluster always requires interactive confirmation.
destroy:
	cd $(TERRAFORM_DIR) && terraform destroy
