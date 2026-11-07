# Changelog

## [0.6.0](https://github.com/maxemiliang/maxcloud/compare/functions-js/v0.5.0...functions-js/v0.6.0) (2026-11-07)


### Features

* **functions:** OnShutdown hooks in the Go SDK ([47b72fb](https://github.com/maxemiliang/maxcloud/commit/47b72fbe6e17eff4d6336c22f0959f1b969da9a4))


### Bug Fixes

* bugs from the docs audit (deploy mode, migrations table, restore-to, Go shutdown/pool, small things) ([bb61573](https://github.com/maxemiliang/maxcloud/commit/bb615731a3eb4934bd43b64f1fa2553475085400))
* **functions-js:** build req.url from the Host header, not X-Forwarded-Host ([e5c28a0](https://github.com/maxemiliang/maxcloud/commit/e5c28a0aed3abc5975545a588a9fdd836885f41e))
* **functions:** describe() leaves out a drizzle-kit migrations folder ([b82bcab](https://github.com/maxemiliang/maxcloud/commit/b82bcab9be5669ea7885b6ef301fe949319a2d50))
* **functions:** KVList and kv.list agree on a limit of 0 ([42a643e](https://github.com/maxemiliang/maxcloud/commit/42a643e6373a32965384b5717b5d5351d2294cd9))
* medium findings from the 2026-11-06 security audit ([b7fa76c](https://github.com/maxemiliang/maxcloud/commit/b7fa76cc8cdda74e41eaefa4a7429c3d3bb07f6e))
* **migrate:** record migrations in the connection's schema, not always public ([4bc6335](https://github.com/maxemiliang/maxcloud/commit/4bc63353787924e7df612edd1e42882f8b1e8491))

## [0.5.0](https://github.com/maxemiliang/maxcloud/compare/functions-js/v0.4.0...functions-js/v0.5.0) (2026-11-06)


### Features

* custom domains, Nuxt on maxcloud, migrations, source:, release-please ([54f28da](https://github.com/maxemiliang/maxcloud/commit/54f28dab3f675b2b868c8f1cde1a468acfe770ce))
* **functions-js:** Node listeners, start(), onShutdown ([9621a81](https://github.com/maxemiliang/maxcloud/commit/9621a81d89b4617faa8ba728e6f094970154ea46))
* **functions:** @max-cloud/functions/testing: a throwaway database and in-memory buckets ([2cf0fac](https://github.com/maxemiliang/maxcloud/commit/2cf0facb6b62ccbf2c651c59bc64f9a319ba0a6d))
* **functions:** proxied, memoryLimit, user, read-only root, volumes, healthPath ([e7dbaf3](https://github.com/maxemiliang/maxcloud/commit/e7dbaf319ec3491c50032e4423748d17b9d12e14))
* SQL migrations (mcloud db migrate, mcloud dev, applied on start) ([6907b09](https://github.com/maxemiliang/maxcloud/commit/6907b092b6f5b401e47ea5abbefd34a5b098528d))
