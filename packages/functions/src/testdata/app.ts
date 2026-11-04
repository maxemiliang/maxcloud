// A small app for the end-to-end tests.
import { onQueue, onRequest, onSchedule } from "../index.ts";

export const hello = onRequest({ public: true }, async (req) => {
  const wait = Number(new URL(req.url).searchParams.get("wait") ?? 0);
  await new Promise((r) => setTimeout(r, wait));
  return new Response("hello\n");
});

export const nightlyReport = onSchedule("0 3 * * *", async () => {
  if (process.env.FAIL) throw new Error("report failed");
});

export const resize = onQueue("images", async () => {});

export const notAFunction = 42;
