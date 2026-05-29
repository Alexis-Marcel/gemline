#!/usr/bin/env python3
"""Dynamic Ansible inventory built from `terraform output -json` in the
sibling deploy/terraform/ dir. Uses the _meta.hostvars shortcut so Ansible
calls it once (--list) instead of N+1 times.
"""

import json
import os
import subprocess
import sys
from pathlib import Path


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

    Handles both the legacy single-CP layout (cp_count == 1) and the
    HA layout (cp_count >= 3). The dimensioning is driven entirely by
    the lists returned by Terraform; this script doesn't recompute IP
    allocation, just consumes what TF has already decided.
    """
    # Fall back to the older scalar control_plane_ipv4 for pre-HA state.
    cp_public_ips = outputs.get(
        "control_plane_ipv4_all",
        [outputs["control_plane_ipv4"]] if "control_plane_ipv4" in outputs else [],
    )
    cp_private_ips = outputs.get("control_plane_private_ips", [])
    worker_public_ips = outputs.get("worker_ipv4", [])
    worker_private_ips = outputs.get("worker_private_ips", [])
    lb_ipv4 = outputs.get("load_balancer_ipv4", "")

    # lb_ipv4 is shared so k3s_server/k3s_agent can target the LB for
    # --tls-san and --server flags.
    common_vars = {
        "ansible_user": "root",
        "ansible_python_interpreter": "/usr/bin/python3",
        "private_network_cidr": "10.0.0.0/16",
        "private_network_gateway": "10.0.0.1",
        "private_iface": "enp7s0",
        "lb_ipv4": lb_ipv4,
    }

    # Single-CP keeps the historical "gemline-cp" name; multi-CP uses cp1/cp2/...
    if len(cp_public_ips) == 1:
        cp_hostnames = ["gemline-cp"]
    else:
        cp_hostnames = [f"gemline-cp{i + 1}" for i in range(len(cp_public_ips))]

    hostvars = {}
    for i, name in enumerate(cp_hostnames):
        # Fall back to computed private IP if the output is missing (older state).
        priv = cp_private_ips[i] if i < len(cp_private_ips) else f"10.0.1.{10 + i}"
        hostvars[name] = {
            "ansible_host": cp_public_ips[i],
            "private_ip": priv,
        }

    worker_hostnames = []
    for i, w_ip in enumerate(worker_public_ips):
        name = f"gemline-w{i + 1}"
        worker_hostnames.append(name)
        priv = (
            worker_private_ips[i]
            if i < len(worker_private_ips)
            else f"10.0.1.{10 + len(cp_public_ips) + i}"
        )
        hostvars[name] = {
            "ansible_host": w_ip,
            "private_ip": priv,
        }

    return {
        "_meta": {"hostvars": hostvars},
        "all": {"vars": common_vars},
        "control_plane": {"hosts": cp_hostnames},
        "workers": {"hosts": worker_hostnames},
        "k3s_cluster": {
            "children": ["control_plane", "workers"],
        },
    }


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] not in ("--list", "--host"):
        sys.stderr.write("usage: inventory.tf.py --list | --host <name>\n")
        sys.exit(1)

    if sys.argv[1] == "--host":
        # Host vars already live in --list output under _meta.hostvars.
        print("{}")
        return

    outputs = terraform_outputs()
    print(json.dumps(build_inventory(outputs), indent=2))


if __name__ == "__main__":
    main()
