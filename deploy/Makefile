# Orchestration des opérations infra. Tu lances `make <target>` depuis
# le dossier `deploy/`. Un seul point d'entrée pour tout le flow
# "provisioner + configurer", au lieu de jongler entre 2 dossiers et
# 6 commandes.
#
# Usage :
#   make help                liste les targets dispo
#   make deploy              terraform apply + ansible-playbook (le flow complet)
#   make plan                terraform plan (dry-run)
#   make ansible             ansible-playbook seul (déjà provisionné)
#   make ansible-check       ansible-playbook --check (dry-run config)
#   make ssh-cp / ssh-w1     ouvrir un shell SSH sur un node
#   make kubeconfig          récupérer le kubeconfig dans ~/.kube/gemline.yaml
#   make destroy             terraform destroy (avec confirmation)

# Variables partagées entre targets.
TERRAFORM_DIR := terraform
ANSIBLE_DIR   := ansible
KUBECONFIG_LOCAL := $(HOME)/.kube/gemline.yaml

# `.PHONY` dit à make que ces targets ne produisent PAS un fichier du
# même nom (sinon il pourrait croire que `make deploy` est déjà fait
# si un fichier "deploy" existe).
.PHONY: help deploy plan ansible ansible-check ssh-cp ssh-w1 kubeconfig destroy

# La target par défaut quand on tape juste `make` : affiche l'aide.
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

# Le flow complet "from zero" : provisionne l'infra puis la configure.
# Si terraform a déjà appliqué, c'est un no-op + Ansible re-réconcilie.
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

# Ansible playbook. --ask-vault-pass demande le password Vault à la
# saisie. Pour skip : exporter ANSIBLE_VAULT_PASSWORD_FILE=... avant.
ansible:
	cd $(ANSIBLE_DIR) && ansible-playbook playbook.yaml --ask-vault-pass

ansible-check:
	cd $(ANSIBLE_DIR) && ansible-playbook playbook.yaml --check --ask-vault-pass

# Helpers SSH. On lit l'IP depuis terraform output pour éviter de la
# retaper. `terraform output -raw` donne juste la valeur sans guillemets.
ssh-cp:
	ssh root@$$(cd $(TERRAFORM_DIR) && terraform output -raw control_plane_ipv4)

# Le worker_ipv4 est une liste ; on prend le 1er élément via jq.
ssh-w1:
	ssh root@$$(cd $(TERRAFORM_DIR) && terraform output -json worker_ipv4 | jq -r '.[0]')

# Récupère le kubeconfig du CP et substitue l'IP loopback par l'IP
# publique pour qu'il marche depuis ton laptop.
kubeconfig:
	@CP_IP=$$(cd $(TERRAFORM_DIR) && terraform output -raw control_plane_ipv4); \
	ssh root@$$CP_IP cat /etc/rancher/k3s/k3s.yaml \
	  | sed "s/127.0.0.1/$$CP_IP/" > $(KUBECONFIG_LOCAL); \
	chmod 600 $(KUBECONFIG_LOCAL); \
	echo "Wrote kubeconfig to $(KUBECONFIG_LOCAL)"; \
	echo "Run:  export KUBECONFIG=$(KUBECONFIG_LOCAL)"

# Détruit l'infra. Tu dois confirmer à l'interactif (`terraform destroy`
# demande "yes" à la saisie). Pas de --auto-approve sur cette target —
# détruire un cluster prod sans confirmation, c'est non.
destroy:
	cd $(TERRAFORM_DIR) && terraform destroy
