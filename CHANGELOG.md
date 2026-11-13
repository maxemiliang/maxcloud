# Changelog

## [0.16.1](https://github.com/maxemiliang/maxcloud/compare/v0.16.0...v0.16.1) (2026-11-13)


### Bug Fixes

* **mcloud:** help, maxcloud.yaml schema and docs name makiya.dev as apps' domain ([64e93af](https://github.com/maxemiliang/maxcloud/commit/64e93af92f9f5ba8815366581cd269b7c2823666))

## [0.16.0](https://github.com/maxemiliang/maxcloud/compare/v0.15.0...v0.16.0) (2026-11-11)


### Features

* **mcloud:** sandbox forward, a session's ports on this machine ([bf1270c](https://github.com/maxemiliang/maxcloud/commit/bf1270cb158202ad8d4e32a03d5a2a4ac46bdbd3))
* **mcloud:** sandbox ssh and ssh-config: SSH into sessions over exec ([86b4b55](https://github.com/maxemiliang/maxcloud/commit/86b4b55852a4302554d90d721086245b2d1982b0))


### Bug Fixes

* **mcloud:** sandbox ssh: forwarded ports listen on loopback, whatever Host * says ([a48479a](https://github.com/maxemiliang/maxcloud/commit/a48479a52551d480ada5d5aa33547c4a415bb058))
* **mcloud:** sandbox ssh: the no-SSH-server message names no image tag ([1db22e8](https://github.com/maxemiliang/maxcloud/commit/1db22e845598c22bec8d4e73778beb2b4a50ed61))
* **mcloud:** sandbox start doesn't name a size for the image it waits on ([9905770](https://github.com/maxemiliang/maxcloud/commit/9905770053611c0404859574e64b2f27b90ccc0f))

## [0.15.0](https://github.com/maxemiliang/maxcloud/compare/v0.14.2...v0.15.0) (2026-11-11)


### Features

* **mcloud:** mcloud sandbox, Claude Code sessions in the cluster ([700817d](https://github.com/maxemiliang/maxcloud/commit/700817dfa95161c702229810556adf448e8c9a77))
* **mcloud:** sandbox start --from, prompt and pull ([505744b](https://github.com/maxemiliang/maxcloud/commit/505744b4361111fce1a38f98a48eefc1ad3f5ad8))


### Bug Fixes

* **mcloud:** sandbox cp leaves git's own files in the session ([3abd141](https://github.com/maxemiliang/maxcloud/commit/3abd1411ab72a5ab40897aaec66025df343443ac))
* **mcloud:** sandbox cp: an NTFS stream of .git is .git too ([71f6e58](https://github.com/maxemiliang/maxcloud/commit/71f6e582e00a78c0ab17afc1ad6a7a762991cc8c))

## [0.14.2](https://github.com/maxemiliang/maxcloud/compare/v0.14.1...v0.14.2) (2026-11-09)


### Bug Fixes

* **mcloud:** find the app's deploy workflow by its run-deploy call, not its name ([01a63b3](https://github.com/maxemiliang/maxcloud/commit/01a63b31a9de3e8b53c5722054fe01a6f50892bd))
* **mcloud:** find the GitHub repo when insteadOf rewrites origin to an SSH alias ([51a1c9c](https://github.com/maxemiliang/maxcloud/commit/51a1c9c1b626aa03b0f5ebfaead3d9618a1b95c9))

## [0.14.1](https://github.com/maxemiliang/maxcloud/compare/v0.14.0...v0.14.1) (2026-11-09)


### Bug Fixes

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
