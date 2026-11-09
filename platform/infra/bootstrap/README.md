# infra/bootstrap

Run once. Creates what every other stack depends on:

| What | How |
|---|---|
| Restricted UpCloud API sub-account + token | `upctl` (the tofu provider has no resource for accounts or tokens) |
| Managed Object Storage instance `homelab` (europe-1, Helsinki) | tofu |
| `tfstate` bucket + `tofu-state` S3 user limited to that bucket | tofu |

State is **local** (`terraform.tfstate`, gitignored) and encrypted with
`TF_VAR_state_passphrase`. If you lose this state, nothing breaks: the resources keep
running and can be brought back with `tofu import`.

Commands below are for fish.

## 1. API sub-account (as your main account)

```fish
upctl account token list            # note your main token's ID, to delete later
set MY_IP (curl -4 -s https://ifconfig.me)
read -s -P 'Sub-account password: ' -x SUB_PW   # 12+ chars, upper, lower, digit

upctl account create --username homelab_api --password $SUB_PW \
  --ip-filter $MY_IP \
  --permission 'managed_object_storage:*' --permission 'managed_kubernetes:*' \
  --permission 'network:*' --permission 'router:*' --permission 'network_gateway:*' \
  --permission 'managed_loadbalancer:*' --permission 'server:*' --permission 'storage:*'
```

## 2. Token for the sub-account

```fish
# Check that upctl really acts as the sub-account (username should be homelab_api)
env UPCLOUD_USERNAME=homelab_api UPCLOUD_PASSWORD=$SUB_PW upctl account show

# Create a 90-day token limited to your IP and store it in the keyring.
# The token goes straight through the pipe and is never printed.
env UPCLOUD_USERNAME=homelab_api UPCLOUD_PASSWORD=$SUB_PW \
  upctl account token create --name tofu-laptop --expires-in 2160h \
  --allow-ip-range $MY_IP/32 -o json \
  | jq -r .token | upctl account login --with-token

set -e SUB_PW
upctl account show                  # should now show homelab_api
```

Then delete the main account's API token in the Hub (Account → API tokens).
Save the sub-account password in your password manager.

## 3. Apply

```fish
cd infra/bootstrap
tofu init
tofu plan -out tfplan
tofu apply tfplan
```

## 4. Outputs for the next stacks

```fish
tofu output                          # endpoint, bucket, key id (secret is hidden)
tofu output -raw state_secret_access_key   # only when filling in .env
```

## When your IP changes

The sub-account, the token and (later) the UKS control plane are all limited to
your IP. Log in to the Hub as the main account, update the sub-account's IP
filter, then rerun step 2 with the new `MY_IP`.
