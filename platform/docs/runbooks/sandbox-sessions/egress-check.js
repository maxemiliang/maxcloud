// What a session's pod can reach. Run it in one (see ../sandbox-sessions.md):
//   mcloud sandbox shell <session> -- node - [HOST:PORT...] < docs/runbooks/sandbox-sessions/egress-check.js
// Every HOST:PORT given is something in the cluster that must not answer:
// a node's kubelet, a pod.
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
  // Allowed by the policy in sandbox/sessions/users.yaml: the internet.
  await t("dns api.anthropic.com", dns.lookup("api.anthropic.com").then((r) => r.address));
  await t("https api.anthropic.com", get("https://api.anthropic.com/v1/models")); // 401: reached, no key
  await t("https registry.npmjs.org", get("https://registry.npmjs.org/-/ping"));
  await t("https github.com", get("https://github.com/"));
  await t("tcp github.com:22", tcp("github.com", 22));
  // Not allowed: the metadata service, and the cluster. A ClusterIP times
  // out under gVisor whatever the policy says, so the lines that test the
  // policy are the pod and node addresses given as arguments.
  await t("tcp 169.254.169.254:80 (metadata)", tcp("169.254.169.254", 80));
  await t("tcp 10.96.0.1:443 (API Service)", tcp("10.96.0.1", 443));
  for (const target of process.argv.slice(2)) {
    const i = target.lastIndexOf(":");
    await t("tcp " + target, tcp(target.slice(0, i), Number(target.slice(i + 1))));
  }
  process.exit(0);
})();
