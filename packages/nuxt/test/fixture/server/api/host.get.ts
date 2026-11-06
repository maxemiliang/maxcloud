export default defineEventHandler((event) => ({ host: getRequestHeader(event, "host"), url: getRequestURL(event).href }));
