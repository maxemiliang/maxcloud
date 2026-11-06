// Three chunks, 300 ms apart: a client sees the first long before the last.
export default defineEventHandler((event) => {
  setResponseHeader(event, "content-type", "text/plain; charset=utf-8");
  let i = 0;
  return new ReadableStream({
    async pull(controller) {
      if (i > 0) await new Promise((r) => setTimeout(r, 300));
      if (i === 3) return controller.close();
      controller.enqueue(new TextEncoder().encode(`chunk ${i++} ${Date.now()}\n`));
    },
  });
});
