# Single entrypoint for the provision + configure flow. Run from deploy/.
#   make deploy   terraform apply + ansible-playbook (full bootstrap)
#   make help     list targets

TERRAFORM_DIR := terraform
ANSIBLE_DIR   := ansible
KUBECONFIG_LOCAL := $(HOME)/.kube/gemline.yaml

.PHONY: help deploy plan ansible ansible-check ssh-cp ssh-w1 kubeconfig destroy
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

# Fetch the CP kubeconfig and rewrite the loopback address to the public IP.
kubeconfig:
	@CP_IP=$$(cd $(TERRAFORM_DIR) && terraform output -raw control_plane_ipv4); \
	ssh root@$$CP_IP cat /etc/rancher/k3s/k3s.yaml \
	  | sed "s/127.0.0.1/$$CP_IP/" > $(KUBECONFIG_LOCAL); \
	chmod 600 $(KUBECONFIG_LOCAL); \
	echo "Wrote kubeconfig to $(KUBECONFIG_LOCAL)"; \
	echo "Run:  export KUBECONFIG=$(KUBECONFIG_LOCAL)"

# No --auto-approve: destroying a cluster always requires interactive confirmation.
destroy:
	cd $(TERRAFORM_DIR) && terraform destroy
