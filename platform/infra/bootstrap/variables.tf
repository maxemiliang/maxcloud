variable "state_passphrase" {
  description = "Passphrase for OpenTofu state encryption (min 16 chars). Set via TF_VAR_state_passphrase."
  type        = string
  sensitive   = true
  # Ephemeral: never written to plan or state files.
  ephemeral = true

  validation {
    condition     = length(var.state_passphrase) >= 16
    error_message = "The state passphrase must be at least 16 characters."
  }
}

variable "object_storage_region" {
  description = "Managed Object Storage region. europe-1 has its primary zone in fi-hel2 (Helsinki)."
  type        = string
  default     = "europe-1"
}

variable "state_bucket_name" {
  description = "Bucket that holds OpenTofu state for the other stacks."
  type        = string
  default     = "tfstate"
}
