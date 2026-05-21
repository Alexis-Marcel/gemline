# tflint config — Terraform linter for this module.
#
# Run locally:
#   cd deploy/terraform
#   tflint --init    # download plugins (once)
#   tflint           # lint the module
#
# CI runs the same two commands in .github/workflows/terraform-plan.yml.

config {
  # Treat plugins as required: fail if a plugin can't be loaded rather
  # than silently skipping its rules.
  call_module_type = "all"
}

# Built-in Terraform rules: naming conventions, unused declarations,
# version pinning checks, deprecated syntax, etc.
plugin "terraform" {
  enabled = true
  preset  = "recommended"
}
