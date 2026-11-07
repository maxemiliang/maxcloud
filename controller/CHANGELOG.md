# Changelog

## [0.12.0](https://github.com/maxemiliang/maxcloud/compare/controller/v0.11.0...controller/v0.12.0) (2026-11-07)


### Features

* **controller:** back up apps' env (ConfigMap app-env) ([acbd4c3](https://github.com/maxemiliang/maxcloud/commit/acbd4c3c6d6ddf1561216f3ea660500c15a3f566))
* **controller:** Traefik's safe naming for public bucket routes ([1a3d7ee](https://github.com/maxemiliang/maxcloud/commit/1a3d7eefb58dc637ac3d4e89646a911e3bf87d9c))
* plain environment variables (env: in maxcloud.yaml, mcloud env) ([08e5f08](https://github.com/maxemiliang/maxcloud/commit/08e5f086f984f4693f96b15a81a67a07c047fadd))


### Bug Fixes

* bugs from the docs audit (deploy mode, migrations table, restore-to, Go shutdown/pool, small things) ([bb61573](https://github.com/maxemiliang/maxcloud/commit/bb615731a3eb4934bd43b64f1fa2553475085400))
* **controller:** cap object reads at 8 MiB ([8dceb69](https://github.com/maxemiliang/maxcloud/commit/8dceb6995e64e03e59d1fd33fb28b9badfdb01a9))
* **controller:** delete a removed app's secrets copies wherever its entry lived ([28c7510](https://github.com/maxemiliang/maxcloud/commit/28c7510441ebb931d3922cbf5bd3e393ef4d6501))
* **controller:** honour restore-to when the Database's Cluster is already gone ([6fcdf60](https://github.com/maxemiliang/maxcloud/commit/6fcdf60935f457acb8f425499dff5a6d6e7e69bc))
* **controller:** refuse maxcloud.yaml values the registry can't render ([c9e473d](https://github.com/maxemiliang/maxcloud/commit/c9e473d6bdfee4163c4c390977a986537f99a7ff))
* **mcloud:** deploy follows the registry entry's mode when maxcloud.yaml has no deploy: ([9a9d95e](https://github.com/maxemiliang/maxcloud/commit/9a9d95e9d443b5b76b117c011e45b89f2786e336))
* medium findings from the 2026-11-06 security audit ([b7fa76c](https://github.com/maxemiliang/maxcloud/commit/b7fa76cc8cdda74e41eaefa4a7429c3d3bb07f6e))

## [0.11.0](https://github.com/maxemiliang/maxcloud/compare/controller/v0.10.0...controller/v0.11.0) (2026-11-06)


### Features

* **controller:** Domains, and bucket names in maxcloud-s3 ([31e7403](https://github.com/maxemiliang/maxcloud/commit/31e74030d87c3daddb9c59fd22c5d15edeb4754c))
* custom domains, Nuxt on maxcloud, migrations, source:, release-please ([54f28da](https://github.com/maxemiliang/maxcloud/commit/54f28dab3f675b2b868c8f1cde1a468acfe770ce))
