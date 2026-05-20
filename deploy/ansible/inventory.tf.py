#!/usr/bin/env python3
"""Dynamic Ansible inventory sourced from Terraform outputs.

Ansible's contract for an executable inventory script:
  * Called with --list  → print JSON for the full inventory
  * Called with --host X → print JSON for that host's vars (or {} if
    everything's already in --list under "_meta.hostvars")

We use the "_meta.hostvars" shortcut so Ansible only calls us once
(--list), not N+1 times.

Reads Terraform outputs from the sibling deploy/terraform/ dir via
`terraform output -json`. The CP IP, worker IPs, and private network
config all come from there → no more IPs hardcoded in inventory.yaml.

Usage from Ansible:
  ANSIBLE_INVENTORY=./inventory.tf.py ansible-playbook playbook.yaml
or (via ansible.cfg):
  [defaults]
  inventory = inventory.tf.py
"""

import json
import os
import subprocess
import sys
from pathlib import Path


# Resolve where Terraform lives, relative to this script. Walking up
# from deploy/ansible/inventory.tf.py → deploy/terraform/.
SCRIPT_DIR = Path(__file__).resolve().parent
TF_DIR = SCRIPT_DIR.parent / "terraform"


def terraform_outputs() -> dict:
    """Run `terraform output -json` and return the parsed dict.

    `terraform output -json` shape:
      { "control_plane_ipv4": {"value": "1.2.3.4", "type": "string"},
        "worker_ipv4":        {"value": ["1.2.3.5", "..."], "type": ...},
        ... }

    We unwrap each entry to just its value for the caller.
    """
    if not TF_DIR.exists():
        sys.stderr.write(f"terraform dir not found: {TF_DIR}\n")
        sys.exit(1)

    try:
        result = subprocess.run(
            ["terraform", "output", "-json"],
            cwd=TF_DIR,
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        sys.stderr.write("terraform CLI not found in PATH\n")
        sys.exit(1)
    except subprocess.CalledProcessError as e:
        sys.stderr.write(f"terraform output failed: {e.stderr}\n")
        sys.exit(1)

    raw = json.loads(result.stdout)
    return {k: v["value"] for k, v in raw.items()}


def build_inventory(outputs: dict) -> dict:
    """Construct the Ansible-format inventory from TF outputs.

    Matches the structure of the previous static inventory.yaml so the
    rest of Ansible (playbooks, roles, group_vars) doesn't need to change.
    """
    cp_ip = outputs["control_plane_ipv4"]
    worker_ips = outputs.get("worker_ipv4", [])

    # Common vars for all hosts (was: `all.vars` in inventory.yaml)
    common_vars = {
        "ansible_user": "root",
        "ansible_python_interpreter": "/usr/bin/python3",
        "private_network_cidr": "10.0.0.0/16",
        "private_network_gateway": "10.0.0.1",
        "private_iface": "enp7s0",
    }

    # Per-host vars. The private IP convention matches Terraform's
    # static assignment (CP = 10.0.1.10, workers = 10.0.1.{11,12,...}).
    hostvars = {
        "gemline-cp": {
            "ansible_host": cp_ip,
            "private_ip": "10.0.1.10",
        },
    }
    for i, w_ip in enumerate(worker_ips):
        name = f"gemline-w{i + 1}"
        hostvars[name] = {
            "ansible_host": w_ip,
            "private_ip": f"10.0.1.{11 + i}",
        }

    return {
        "_meta": {"hostvars": hostvars},
        "all": {"vars": common_vars},
        "control_plane": {"hosts": ["gemline-cp"]},
        "workers": {
            "hosts": [f"gemline-w{i + 1}" for i in range(len(worker_ips))],
        },
        "k3s_cluster": {
            "children": ["control_plane", "workers"],
        },
    }


def main() -> None:
    # Ansible calls us with --list or --host X. We only support --list
    # (host vars come via _meta.hostvars in the --list output).
    if len(sys.argv) < 2 or sys.argv[1] not in ("--list", "--host"):
        sys.stderr.write("usage: inventory.tf.py --list | --host <name>\n")
        sys.exit(1)

    if sys.argv[1] == "--host":
        # Empty dict — host vars already in --list output.
        print("{}")
        return

    outputs = terraform_outputs()
    print(json.dumps(build_inventory(outputs), indent=2))


if __name__ == "__main__":
    main()
