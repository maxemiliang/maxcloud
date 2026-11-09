locals {
  flux_system_dir = "${path.root}/../../clusters/personal/flux-system"

  # Both Secrets land in flux-system. The module writes them with a write-only
  # attribute (data_wo), so their contents never reach the state file; it only
  # stores a hash, to notice when they change.
  secrets_yaml = join("---\n", [
    yamlencode({
      apiVersion = "v1"
      kind       = "Secret"
      metadata   = { name = "flux-system" } # FluxInstance .spec.sync.pullSecret
      type       = "Opaque"
      stringData = {
        githubAppID                = var.github_app_id
        githubAppInstallationOwner = var.github_app_installation_owner
        githubAppPrivateKey        = file(pathexpand(var.github_app_private_key_path))
      }
    }),
    yamlencode({
      apiVersion = "v1"
      kind       = "Secret"
      # kustomize-controller decrypts SOPS files with this. The key name must
      # end in .agekey.
      metadata   = { name = "sops-age" }
      type       = "Opaque"
      stringData = { "age.agekey" = file(pathexpand(var.age_key_path)) }
    }),
  ])
}

# Tofu manages only the bootstrap mechanism: a Job that installs the Flux
# Operator and applies the FluxInstance if they're missing, then hands both
# over to Flux. Afterwards Flux reconciles them from clusters/personal/flux-system.
module "flux_operator_bootstrap" {
  source  = "controlplaneio-fluxcd/flux-operator-bootstrap/kubernetes"
  version = "0.9.0"

  revision = var.bootstrap_revision

  gitops_resources = {
    instance_yaml = file("${local.flux_system_dir}/flux-instance.yaml")
    operator_chart = {
      # Keep in step with the OCIRepository semver in flux-operator.yaml.
      version     = "0.62.1"
      values_yaml = file("${local.flux_system_dir}/flux-operator-values.yaml")
    }
  }

  managed_resources = {
    secrets_yaml = local.secrets_yaml
  }

  # On failure, relay the bootstrap Job's logs into the tofu output (uses kubectl
  # with the KUBECONFIG that mise sets).
  debug_on_failure = true
}
