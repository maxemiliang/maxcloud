import { strict as assert } from "node:assert";
import { test } from "node:test";
import { bucketName } from "./storage.ts";

test("bucketName prefixes the app", () => {
  assert.equal(bucketName("uploads", { MAXCLOUD_APP: "shop" }), "mc-shop--uploads");
});

test("bucketName without MAXCLOUD_APP explains", () => {
  assert.throws(() => bucketName("uploads", {}), /MAXCLOUD_APP isn't set/);
});

test("bucketName rejects names the registry wouldn't have", () => {
  for (const bad of ["a--b", "Up", "-x", "x-", "a.b", ""]) {
    assert.throws(() => bucketName(bad, { MAXCLOUD_APP: "shop" }), /single dashes/, bad);
  }
});
