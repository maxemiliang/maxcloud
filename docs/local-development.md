# Local development

`mcloud dev` runs the whole app on your machine, with what it gets on
maxcloud:

- every HTTP function at `http://<name>.localhost:8080/` (a list at
  `http://localhost:8080/`), the queue workers, and scheduled functions on
  `POST http://localhost:8080/_run/<name>`;
- `DATABASE_URL`: a local Postgres in Docker (kept between runs; the
  default when `maxcloud.yaml` has `db:`), or a branch of the app's
  database through a port-forward (`--db branch:NAME`), with the pending
  migrations applied;
- `MAXCLOUD_APP`, `maxcloud.yaml`'s `env:`, and secrets from `.env` (keep
  it out of git; it overrides `env:` here), or the app's own secrets and
  [`mcloud env`](environment.md) values (`--secrets remote`); the app's
  real buckets only with `--buckets`.

It sets `MAXCLOUD_DEV=1`, and reads `.env` from the repo's root and from
`source:` (`--secrets remote` values win over both).

TypeScript runs from source and restarts on changes; Go runs with
`go run .` and doesn't restart; a Nuxt app (a
`nuxt.config`) runs `nuxt dev` on the port instead. To poke the deployed
app: `mcloud call api /health` sends a request to any function, private
ones too, and `mcloud run nightly` runs a scheduled function now.
