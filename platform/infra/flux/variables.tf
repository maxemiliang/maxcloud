variable "state_passphrase" {
  description = "Passphrase for OpenTofu state encryption (min 16 chars). Set via TF_VAR_state_passphrase."
  type        = string
  sensitive   = true
  ephemeral   = true

  validation {
    condition     = length(var.state_passphrase) >= 16
    error_message = "The state passphrase must be at least 16 characters."
  }
}

variable "bootstrap_revision" {
  description = "Bump to re-run the bootstrap Job when no input changed, e.g. to repair a broken Flux install. Changed inputs (including secret contents) re-run it automatically."
  type        = number
  default     = 1
}

variable "github_app_id" {
  description = "GitHub App ID (not secret). Shown on the app's settings page."
  type        = string
}

variable "github_app_installation_owner" {
  description = "Account the app is installed on; Flux looks up the installation ID from this."
  type        = string
  default     = "maxemiliang"
}

variable "github_app_private_key_path" {
  description = "Path to the GitHub App private key (PEM), outside the repo."
  type        = string
  default     = "~/.config/homelab/flux-github-app.pem"
}

variable "age_key_path" {
  description = "Path to the age private key used by SOPS. Becomes the sops-age Secret."
  type        = string
  default     = "~/.config/sops/age/keys.txt"
}
