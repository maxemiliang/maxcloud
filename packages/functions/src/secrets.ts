// The app's secrets (mcloud secrets set NAME) reach every function as
// environment variables. secret() reads one, and says what to do when it's
// missing instead of handing back undefined.

/**
 * The value of the app's secret `name`:
 *
 * ```ts
 * import { secret } from "@max-cloud/functions";
 * const stripe = new Stripe(secret("STRIPE_KEY"));
 * ```
 *
 * Throws if it isn't set. Read it where it's used (in a handler, or lazily),
 * not at module load: `maxcloud-functions --describe` loads the module
 * without the app's secrets.
 */
export function secret(name: string, env: Record<string, string | undefined> = process.env): string {
  const v = env[name];
  if (v === undefined || v === "") {
    throw new Error(`secret ${name} isn't set: mcloud secrets set ${name} (in the app's repo), then deploy`);
  }
  return v;
}
