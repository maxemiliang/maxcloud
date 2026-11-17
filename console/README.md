# maxcloud console

The web console, `https://console.makiya.dev`: a read-only site where a
developer logs in and sees their apps. A Nuxt app whose server holds the
login and reads the cluster as the logged-in user.

```fish
npm ci
npm run dev        # http://localhost:3000, made-up apps, no cluster
npm test
```

What it shows, how it works, its configuration and the other test
commands: [docs/console.md](../docs/console.md). Running it on the
platform: [the platform README](../platform/README.md#procedures), "The
web console".

It isn't one of the npm workspaces (those are the published packages) and
release-please doesn't release it: its version is `version` in
`package.json`, which `.github/workflows/console-image.yaml` turns into the
image's tag.
