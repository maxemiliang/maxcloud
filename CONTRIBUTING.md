# Working on maxcloud

## Development

```fish
go test ./...
go run ./cmd/mcloud --help

npm ci
npm run build --workspaces   # @max-cloud/nuxt uses the functions SDK's build
# The queue tests need a Postgres; without TEST_DATABASE_URL they're skipped.
docker run -d --rm --name pg -p 127.0.0.1:55432:5432 -e POSTGRES_PASSWORD=test postgres:18
set -x TEST_DATABASE_URL postgres://postgres:test@127.0.0.1:55432/postgres
npm test --workspaces
```

The controller is its own Go module, with its own steps:
[controller/README.md](controller/README.md#development).

## Docs

The guides in [`docs/`](docs/) are written by hand: change them in the same
PR as the behaviour they describe. [`docs/reference/`](docs/reference/) is
generated, from

- the commands' help (`Short`, `Long`, `Example` and flag usage in
  `internal/cli`),
- [`maxcloud.schema.json`](maxcloud.schema.json) for `maxcloud.yaml` (a
  test checks it names exactly the fields mcloud reads),
- the controller's CRDs in `controller/config/crd`, which controller-gen
  writes from the comments in `controller/api`.

After changing any of them:

```fish
go run ./internal/gendocs
```

CI fails if `docs/reference/` isn't what that writes.

## Releasing

release-please (`.github/workflows/release-please.yaml`) keeps one release
PR open on `main`. It holds the next version of each part that has
changes, worked out from the conventional commits under that part's
directory (`feat` a minor version, `fix` a patch; while below 1.0.0 a
breaking change is a minor too), with changelogs and the versions in files.
Merging the PR releases those parts:

| Part | Directory | Tag | Released by |
| --- | --- | --- | --- |
| mcloud | everything else | `vX.Y.Z` | `release.yaml`: GoReleaser adds the binaries to the GitHub release |
| controller | `controller/` | `controller/vX.Y.Z` | `release-controller.yaml`: the image (the PR bumps `newTag` in `config/kustomization.yaml`) |
| Go SDK | `functions/` | `functions/vX.Y.Z` | the tag (Go modules) |
| `@max-cloud/functions` | `packages/functions/` | `functions-js/vX.Y.Z` | `release-npm.yaml` (trusted publishing, no token) |
| `@max-cloud/nuxt` | `packages/nuxt/` | `nuxt-js/vX.Y.Z` | `release-npm.yaml`, after `@max-cloud/functions` |

- **Order:** the npm SDK goes before `@max-cloud/nuxt` (it depends on it)
  and before mcloud: `run-deploy.yaml` installs the newest mcloud, and
  `mcloud init` writes the newest SDK into new apps. The PR also bumps
  `sdkFallback` (`internal/cli/sdkversion.go`, for when npm can't be
  asked) with each `@max-cloud/functions` release.
- **Merge the release PR as it is,** and other PRs with a merge commit or a
  squash whose message is a conventional commit: the message decides the
  versions and the changelog.
- **A version of your choice:** `Release-As: 1.0.0` in a commit message's
  footer, for the parts that commit touches.
- **Settings it needs:** "Allow GitHub Actions to create and approve pull
  requests" (Settings, Actions, General). On npmjs.com, each package's
  trusted publisher is the workflow `release-please.yaml` (npm checks the
  calling workflow). The release PR is opened with `GITHUB_TOKEN`, so CI
  doesn't run on it by itself.
- **By hand,** if release-please is stuck: pushing a `v*` or
  `controller/v*` tag still runs its workflow. npm packages only publish
  through release-please.

Config: `release-please-config.json`, and `.release-please-manifest.json`
with the last released versions.
