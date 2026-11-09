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

variable "postgres_backup_bucket" {
  description = "Bucket for CloudNativePG base backups and WAL archives."
  type        = string
  default     = "pg-backups"
}

variable "loki_bucket" {
  description = "Bucket for Loki's log chunks and index."
  type        = string
  default     = "loki"
}
