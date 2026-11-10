// What a pod in sandbox-spike can reach. Run it in a test pod, before and
// after egress.yaml (see ../sandbox-spike.md):
//   kubectl -n sandbox-spike exec -i gvisor -- node - < docs/runbooks/sandbox-spike/egress-check.js
const dns = require("dns").promises, net = require("net"), https = require("https");
const T = 6000;
const t = (name, p) =>
  Promise.race([p, new Promise((r) => setTimeout(() => r("TIMEOUT"), T))])
    .then((v) => console.log(name.padEnd(38) + v))
    .catch((e) => console.log(name.padEnd(38) + "ERR " + (e.code || e.message)));
const tcp = (host, port) =>
  new Promise((res, rej) => {
    const s = net.connect({ host, port }, () => { res("connected"); s.destroy(); });
    s.on("error", rej);
  });
const get = (url) =>
  new Promise((res, rej) => {
    https.get(url, { timeout: T }, (r) => { res("HTTP " + r.statusCode); r.resume(); }).on("error", rej);
  });

(async () => {
  // Allowed by egress.yaml.
  await t("dns api.anthropic.com", dns.lookup("api.anthropic.com").then((r) => r.address));
  await t("dns registry.npmjs.org", dns.lookup("registry.npmjs.org").then((r) => r.address));
  await t("https api.anthropic.com", get("https://api.anthropic.com/v1/models")); // 401: reached, no key
  await t("https registry.npmjs.org", get("https://registry.npmjs.org/-/ping"));
  // Not allowed: another name, the resolvers' own HTTPS (DNS over HTTPS
  // would get round the name list), and the cluster.
  await t("dns example.com", dns.lookup("example.com").then((r) => r.address));
  await t("tcp 1.1.1.1:443", tcp("1.1.1.1", 443));
  await t("tcp 9.9.9.9:443", tcp("9.9.9.9", 443));
  await t("tcp 10.96.0.1:443 (API Service)", tcp("10.96.0.1", 443));
  process.exit(0);
})();
