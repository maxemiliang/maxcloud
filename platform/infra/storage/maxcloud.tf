# Object storage for apps on the run platform (maxcloud): their buckets and
# their databases' backups.
#
# A separate instance from the homelab one (infra/bootstrap), so the maxcloud
# controller's credentials can't reach tfstate, pg-backups or loki. The
# controller holds an API sub-account scoped to this instance only (created
# with upctl; the provider has no resource for accounts) and the S3 user
# below. It creates each app's buckets, S3 user and policy itself; tofu owns
# only the instance and the controller's own user.
#
# Billed from 250 GB like the homelab instance (~€5/month, egress included).

resource "upcloud_managed_object_storage" "maxcloud_apps" {
  name              = "maxcloud-apps"
  region            = data.terraform_remote_state.bootstrap.outputs.object_storage_region
  configured_status = "started"
  labels            = local.labels

  # Public endpoint: the cluster reaches it through the NAT gateway, and the
  # laptop directly (mcloud, debugging).
  network {
    family = "IPv4"
    name   = "public"
    type   = "public"
  }

  # Deleting the instance deletes every app's buckets and backups with it.
  lifecycle {
    prevent_destroy = true
  }
}

# The controller's S3 user: full access to this instance's buckets. It needs
# the S3 API for what UpCloud's API doesn't do: bucket policies (public
# buckets) and the restore marker in each app's database bucket. Full access
# adds nothing to what the controller's API token can already do here (create
# users and keys).
resource "upcloud_managed_object_storage_user" "maxcloud_controller" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  username     = "maxcloud-controller"
}

resource "upcloud_managed_object_storage_user_policy" "maxcloud_controller" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  username     = upcloud_managed_object_storage_user.maxcloud_controller.username
  # One of the instance's built-in policies.
  name = "ECSS3FullAccess"
}

resource "upcloud_managed_object_storage_user_access_key" "maxcloud_controller" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  username     = upcloud_managed_object_storage_user.maxcloud_controller.username
  status       = "Active"
}

# --- The image registry (zot) -------------------------------------------------
# maxcloud's own container registry keeps its images here, so they outlive
# the cluster like the apps' data. A bucket tofu owns (not the controller,
# whose app buckets are mc-*), and an S3 user that can reach only it.

resource "upcloud_managed_object_storage_bucket" "maxcloud_images" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  name         = "maxcloud-images"
  # The UpCloud API deletes buckets even when they still contain objects.
  lifecycle {
    prevent_destroy = true
  }
}

resource "upcloud_managed_object_storage_policy" "maxcloud_images" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  name         = "maxcloud-images-readwrite"
  description  = "Read/write the image registry's maxcloud-images bucket only."

  # The API wants the JSON policy URL-encoded.
  document = urlencode(jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ListImagesBucket"
        Effect = "Allow"
        # ListBucketMultipartUploads: the registry resumes interrupted layer
        # uploads by finding their multipart upload.
        Action   = ["s3:ListBucket", "s3:GetBucketLocation", "s3:ListBucketMultipartUploads"]
        Resource = "arn:aws:s3:::${upcloud_managed_object_storage_bucket.maxcloud_images.name}"
      },
      {
        Sid    = "ReadWriteImageObjects"
        Effect = "Allow"
        # DeleteObject: garbage collection and retention remove old images.
        # PutObjectAcl: zot's S3 driver (Distribution's) sends
        # `x-amz-acl: private` with every write, and has no setting to
        # leave it out; without this permission UpCloud answers 403.
        Action = [
          "s3:GetObject", "s3:PutObject", "s3:PutObjectAcl", "s3:DeleteObject",
          "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts",
        ]
        Resource = "arn:aws:s3:::${upcloud_managed_object_storage_bucket.maxcloud_images.name}/*"
      },
    ]
  }))
}

resource "upcloud_managed_object_storage_user" "zot" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  username     = "zot"
}

resource "upcloud_managed_object_storage_user_policy" "zot" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  username     = upcloud_managed_object_storage_user.zot.username
  name         = upcloud_managed_object_storage_policy.maxcloud_images.name
}

resource "upcloud_managed_object_storage_user_access_key" "zot" {
  service_uuid = upcloud_managed_object_storage.maxcloud_apps.id
  username     = upcloud_managed_object_storage_user.zot.username
  status       = "Active"
}

output "zot_access_key_id" {
  value = upcloud_managed_object_storage_user_access_key.zot.access_key_id
}

output "zot_secret_access_key" {
  value     = upcloud_managed_object_storage_user_access_key.zot.secret_access_key
  sensitive = true
}

output "maxcloud_apps_id" {
  description = "Instance UUID: the controller sub-account's permission target."
  value       = upcloud_managed_object_storage.maxcloud_apps.id
}

output "maxcloud_apps_endpoint" {
  value = one([
    for e in upcloud_managed_object_storage.maxcloud_apps.endpoint : "https://${e.domain_name}"
    if e.type == "public"
  ])
}

output "maxcloud_controller_access_key_id" {
  value = upcloud_managed_object_storage_user_access_key.maxcloud_controller.access_key_id
}

output "maxcloud_controller_secret_access_key" {
  value     = upcloud_managed_object_storage_user_access_key.maxcloud_controller.secret_access_key
  sensitive = true
}
