// Serving an HTTP function: Node's http server in front of a fetch-style
// handler (Request in, Response out).

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import type { HttpFunction } from "./define.ts";

function toRequest(req: IncomingMessage, signal: AbortSignal): Request {
  // Traefik and Knative's queue-proxy set these for the original request.
  const proto = String(req.headers["x-forwarded-proto"] ?? "http").split(",")[0]!.trim();
  const host = req.headers["x-forwarded-host"] ?? req.headers.host ?? "localhost";
  const url = new URL(req.url ?? "/", `${proto}://${host}`);
  const headers = new Headers();
  for (let i = 0; i < req.rawHeaders.length; i += 2) {
    headers.append(req.rawHeaders[i]!, req.rawHeaders[i + 1]!);
  }
  const method = req.method ?? "GET";
  const hasBody = method !== "GET" && method !== "HEAD";
  return new Request(url, {
    method,
    headers,
    body: hasBody ? (Readable.toWeb(req) as ReadableStream<Uint8Array>) : undefined,
    signal,
    // Node needs this for a streamed request body.
    duplex: hasBody ? "half" : undefined,
  });
}

async function writeResponse(res: ServerResponse, response: Response, head: boolean): Promise<void> {
  res.statusCode = response.status;
  if (response.statusText) res.statusMessage = response.statusText;
  const cookies = response.headers.getSetCookie();
  response.headers.forEach((value, key) => {
    if (key !== "set-cookie") res.setHeader(key, value);
  });
  if (cookies.length) res.setHeader("set-cookie", cookies);
  if (!response.body || head) {
    res.end();
    return;
  }
  await pipeline(Readable.fromWeb(response.body as import("node:stream/web").ReadableStream), res);
}

export interface Serving {
  server: Server;
  /** The port it listens on (useful with port 0). */
  port: number;
  /** Stops accepting requests and resolves once those in flight have finished, or `graceMs` passed. */
  close(graceMs?: number): Promise<void>;
}

export async function serveHttp(fn: HttpFunction, port: number, log: (msg: string) => void = console.log): Promise<Serving> {
  const shutdown = new AbortController();
  let inFlight = 0;
  let idle: (() => void) | undefined;

  const server = createServer(async (req, res) => {
    inFlight++;
    const client = new AbortController();
    res.on("close", () => {
      if (!res.writableFinished) client.abort(new Error("client went away"));
    });
    const signal = AbortSignal.any([client.signal, shutdown.signal]);
    try {
      const response = await fn.handler(toRequest(req, signal), { signal });
      if (!(response instanceof Response)) throw new TypeError("handler didn't return a Response");
      await writeResponse(res, response, req.method === "HEAD");
    } catch (err) {
      if (!client.signal.aborted) {
        log(`${req.method} ${req.url}: ${err instanceof Error ? (err.stack ?? err.message) : String(err)}`);
      }
      if (!res.headersSent) {
        res.statusCode = 500;
        res.setHeader("content-type", "text/plain; charset=utf-8");
        res.end("Internal Server Error\n");
      } else {
        res.destroy();
      }
    } finally {
      inFlight--;
      if (inFlight === 0) idle?.();
    }
  });
  // Knative's queue-proxy keeps connections open; let it reuse them a while.
  server.keepAliveTimeout = 65_000;

  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, () => resolve());
  });
  const addr = server.address();
  const actual = typeof addr === "object" && addr ? addr.port : port;

  return {
    server,
    port: actual,
    async close(graceMs = 25_000) {
      const closed = new Promise<void>((r) => server.close(() => r()));
      server.closeIdleConnections();
      if (inFlight > 0) {
        await Promise.race([
          new Promise<void>((r) => (idle = r)),
          new Promise<void>((r) => setTimeout(r, graceMs).unref()),
        ]);
      }
      shutdown.abort(new Error("server shutting down"));
      server.closeAllConnections();
      await closed;
    },
  };
}
