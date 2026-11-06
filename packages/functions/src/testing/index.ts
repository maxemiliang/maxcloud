// @max-cloud/functions/testing: helpers for testing a maxcloud app with a
// real database and in-memory buckets. Its own entry point, so production
// bundles don't include it.

export { testDatabase, TEST_SCHEMA_PREFIX, type TestDatabase, type TestDatabaseOptions } from "./database.ts";
export { testBuckets, type TestBuckets, type TestBucketsOptions, type TestObject } from "./buckets.ts";
