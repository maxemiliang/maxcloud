# Changelog

## [0.6.0](https://github.com/maxemiliang/maxcloud/compare/functions/v0.5.0...functions/v0.6.0) (2026-11-07)


### Features

* **functions:** OnShutdown hooks in the Go SDK ([47b72fb](https://github.com/maxemiliang/maxcloud/commit/47b72fbe6e17eff4d6336c22f0959f1b969da9a4))


### Bug Fixes

* bugs from the docs audit (deploy mode, migrations table, restore-to, Go shutdown/pool, small things) ([bb61573](https://github.com/maxemiliang/maxcloud/commit/bb615731a3eb4934bd43b64f1fa2553475085400))
* **functions:** describe a sub-second Backoff as the worker uses it ([c7602f2](https://github.com/maxemiliang/maxcloud/commit/c7602f227e302842fd76bd9af2d512725c2548da))
* **functions:** Go queue workers claim jobs on a pool of their own ([cd1d8bc](https://github.com/maxemiliang/maxcloud/commit/cd1d8bc36feca9ca22c50a0aa8a8aa4d5abe042e))
* **functions:** KVList and kv.list agree on a limit of 0 ([42a643e](https://github.com/maxemiliang/maxcloud/commit/42a643e6373a32965384b5717b5d5351d2294cd9))
* medium findings from the 2026-11-06 security audit ([b7fa76c](https://github.com/maxemiliang/maxcloud/commit/b7fa76cc8cdda74e41eaefa4a7429c3d3bb07f6e))
* **migrate:** record migrations in the connection's schema, not always public ([4bc6335](https://github.com/maxemiliang/maxcloud/commit/4bc63353787924e7df612edd1e42882f8b1e8491))

## [0.5.0](https://github.com/maxemiliang/maxcloud/compare/functions/v0.4.0...functions/v0.5.0) (2026-11-06)


### Features

* custom domains, Nuxt on maxcloud, migrations, source:, release-please ([54f28da](https://github.com/maxemiliang/maxcloud/commit/54f28dab3f675b2b868c8f1cde1a468acfe770ce))
* **functions:** container options for every kind of function ([02fc8b2](https://github.com/maxemiliang/maxcloud/commit/02fc8b25db173e36fdee6f7e1ec66dd43a033181))
* **functions:** proxied, memoryLimit, user, read-only root, volumes, healthPath ([e7dbaf3](https://github.com/maxemiliang/maxcloud/commit/e7dbaf319ec3491c50032e4423748d17b9d12e14))
* SQL migrations (mcloud db migrate, mcloud dev, applied on start) ([6907b09](https://github.com/maxemiliang/maxcloud/commit/6907b092b6f5b401e47ea5abbefd34a5b098528d))
