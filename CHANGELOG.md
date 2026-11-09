# Changelog

## [0.14.1](https://github.com/maxemiliang/maxcloud/compare/v0.14.0...v0.14.1) (2026-11-09)


### Bug Fixes

* **functions:** readiness probes without periodSeconds, so instances are Ready as soon as they're up ([88cd45c](https://github.com/maxemiliang/maxcloud/commit/88cd45c2fb369685b2ea1ee4a3172919c18230e2))
* **functions:** readiness probes without periodSeconds, so instances are Ready as soon as they're up ([f200f85](https://github.com/maxemiliang/maxcloud/commit/f200f85deb10ae9b8377cfe3fa524434020c6e11))
* **mcloud:** apps kept in git live in maxcloud's platform/ ([ac0e9b3](https://github.com/maxemiliang/maxcloud/commit/ac0e9b3cdf2c90b63a2c9c35241e284c26e83625))

## [0.14.0](https://github.com/maxemiliang/maxcloud/compare/v0.13.0...v0.14.0) (2026-11-08)


### Features

* app owners and registration IDs (audit M10) ([d286278](https://github.com/maxemiliang/maxcloud/commit/d28627845818d17184ded8fcc0d3d556d8985c44))
* **mcloud:** app owners ([33e3439](https://github.com/maxemiliang/maxcloud/commit/33e34392fe4c227ce200d8d2ecd58414b0c445cc))

## [0.13.0](https://github.com/maxemiliang/maxcloud/compare/v0.12.0...v0.13.0) (2026-11-07)


### Features

* **mcloud:** env: in maxcloud.yaml and mcloud env set/get/list/unset ([a3236d3](https://github.com/maxemiliang/maxcloud/commit/a3236d3f98ae97026c83047831efe37a9112f116))
* plain environment variables (env: in maxcloud.yaml, mcloud env) ([08e5f08](https://github.com/maxemiliang/maxcloud/commit/08e5f086f984f4693f96b15a81a67a07c047fadd))


### Bug Fixes

* bugs from the docs audit (deploy mode, migrations table, restore-to, Go shutdown/pool, small things) ([bb61573](https://github.com/maxemiliang/maxcloud/commit/bb615731a3eb4934bd43b64f1fa2553475085400))
* **controller:** refuse maxcloud.yaml values the registry can't render ([c9e473d](https://github.com/maxemiliang/maxcloud/commit/c9e473d6bdfee4163c4c390977a986537f99a7ff))
* **functions:** describe a sub-second Backoff as the worker uses it ([c7602f2](https://github.com/maxemiliang/maxcloud/commit/c7602f227e302842fd76bd9af2d512725c2548da))
* **mcloud:** deploy follows the registry entry's mode when maxcloud.yaml has no deploy: ([9a9d95e](https://github.com/maxemiliang/maxcloud/commit/9a9d95e9d443b5b76b117c011e45b89f2786e336))
* medium findings from the 2026-11-06 security audit ([b7fa76c](https://github.com/maxemiliang/maxcloud/commit/b7fa76cc8cdda74e41eaefa4a7429c3d3bb07f6e))
* **migrate:** record migrations in the connection's schema, not always public ([4bc6335](https://github.com/maxemiliang/maxcloud/commit/4bc63353787924e7df612edd1e42882f8b1e8491))

## [0.12.0](https://github.com/maxemiliang/maxcloud/compare/v0.11.0...v0.12.0) (2026-11-06)


### Features

* custom domains, Nuxt on maxcloud, migrations, source:, release-please ([54f28da](https://github.com/maxemiliang/maxcloud/commit/54f28dab3f675b2b868c8f1cde1a468acfe770ce))
* **functions:** container options for every kind of function ([02fc8b2](https://github.com/maxemiliang/maxcloud/commit/02fc8b25db173e36fdee6f7e1ec66dd43a033181))
* **functions:** proxied, memoryLimit, user, read-only root, volumes, healthPath ([e7dbaf3](https://github.com/maxemiliang/maxcloud/commit/e7dbaf319ec3491c50032e4423748d17b9d12e14))
* **mcloud:** domains in maxcloud.yaml ([ca8caef](https://github.com/maxemiliang/maxcloud/commit/ca8caef4084443f6fe43bd9c448e57d470edb29c))
* **mcloud:** source: in maxcloud.yaml builds the app from a subfolder ([f84d2ef](https://github.com/maxemiliang/maxcloud/commit/f84d2ef6ec4c2914731ef54092b7e42c10c772a8))
* **nuxt:** @max-cloud/nuxt, and mcloud dev for Nuxt apps ([93ca115](https://github.com/maxemiliang/maxcloud/commit/93ca1151a98e91421e3547ae8a8574d2671d7cff))
* SQL migrations (mcloud db migrate, mcloud dev, applied on start) ([6907b09](https://github.com/maxemiliang/maxcloud/commit/6907b092b6f5b401e47ea5abbefd34a5b098528d))
