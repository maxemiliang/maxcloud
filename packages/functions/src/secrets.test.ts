import { strict as assert } from "node:assert";
import { test } from "node:test";
import { secret } from "./secrets.ts";

test("secret reads the variable", () => {
  assert.equal(secret("STRIPE_KEY", { STRIPE_KEY: "sk" }), "sk");
});

test("a missing secret says how to set it", () => {
  assert.throws(() => secret("STRIPE_KEY", {}), /mcloud secrets set STRIPE_KEY/);
  assert.throws(() => secret("STRIPE_KEY", { STRIPE_KEY: "" }), /isn't set/);
});
