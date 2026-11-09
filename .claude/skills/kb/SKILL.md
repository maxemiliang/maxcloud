---
name: kb
description: The platform's (the homelab cluster's) knowledge base (platform/docs/kb/) - look things up in it and record new knowledge in it. Use before debugging or changing anything under platform/ (search for the symptom, component or error first), and whenever something non-obvious is learned about the setup - a gotcha that cost time, a design decision and why, how a component really behaves, a number worth keeping - or when the user says to remember, note, record or add something to the knowledge base.
---

# Knowledge base

`platform/docs/kb/` holds what this setup taught us and isn't obvious from the code:
why things are the way they are, and the traps. One file per piece of
knowledge, listed in `platform/docs/kb/README.md`.

**This repo is public.** An entry about a security hole that is still
open, or how to exploit one, goes in the private repo instead:
`private/docs/kb/<area>/<slug>.md` (the `private/` submodule; same format,
not checked by kb-check). Move it here once the hole is closed, and never
put secret values in either.

It is not for what the repo already says (manifests, the README's procedures,
runbooks in `docs/runbooks/`) or what git history records. Link to those
instead. Session notes and to-do lists don't belong either.

## Looking things up

Before debugging or changing a component, search the entries:

```sh
grep -ril '<symptom, error text, component>' platform/docs/kb/
```

Search by error message fragments, component names (traefik, knative, cnpg,
flux, keda, upcloud, ...) and tags. An entry that matches tells you the cause
and the fix; follow its `where:` to the code. If what you find is outdated,
fix the entry as part of the work.

## Recording knowledge

Record it when:

- something cost real time to figure out (a gotcha);
- a choice was made between options (a decision), with the reason;
- you learned how something actually behaves, or a number that matters (a fact);
- the user asks you to remember or record something about the setup.

Don't record something trivially derived from the code, a one-off typo, or
anything secret (no tokens, passwords, keys, internal IPs beyond what the
repo already shows).

### Steps

1. **Check for an existing entry** (search as above). Update it instead of
   adding a near-duplicate: add the new symptom, correct the fix, bump
   `updated:`. If an entry turns out wrong, fix or delete it, don't leave it.
2. **Write the entry** at `platform/docs/kb/<area>/<slug>.md`:
   - `<area>` is one of the index's sections; add a new one only if none fits.
   - `<slug>` is a few lowercase words naming the subject, e.g.
     `knative-external-probe-port`.
   - Use the template for its kind (below). Keep it short: someone hitting
     the problem at 2 a.m. should get the answer from the first lines.
3. **List it in `platform/docs/kb/README.md`** under its area, as
   `- [Title](area/slug.md): one-line hook`, and link related entries both
   ways (`See also:` lines).
4. **Point the code at it** where that helps: a comment next to the
   workaround, e.g. `# See docs/kb/knative/external-probe-port.md.`
5. **Check it:** `platform/scripts/kb-check.sh` (frontmatter, index, links). It also
   runs in `platform/scripts/validate.sh`.
6. **Commit only when the user asks** (the repo's rule). A knowledge-base
   entry is its own small commit, `docs(kb): ...`, unless it documents a
   change made in the same commit.

### Frontmatter (all kinds)

```yaml
---
title: Knative's external probe needs Traefik's plain-HTTP in-cluster port
kind: gotcha            # gotcha | decision | fact
area: knative           # the docs/kb/ subdirectory
tags: [knative, traefik, gateway-api]
learned: 2026-11-04     # when it was learned (absolute date)
updated: 2026-11-04     # last time the entry was checked or changed
where:                  # repo paths it's about (optional, but usually there)
  - infrastructure/configs/gateway/gateway.yaml
---
```

### gotcha

```markdown
**Symptom:** what you see: the error, the stuck status, the command output.

**Cause:** why it happens.

**Fix:** what we did, and where (file, setting).

**Recognise it:** the quickest check, e.g. a kubectl command or log line.
```

### decision

```markdown
**Decision:** what we chose.

**Why:** the reasons that decided it.

**Rejected:** the alternatives and why not.

**Revisit when:** what would change the answer.
```

### fact

A few sentences or a short list: how it behaves, the numbers, the source
(link to the docs or the measurement).

## Style

Plain, short sentences; the repo's own docs are the model. Absolute dates.
Quote error messages exactly, so they can be found by searching. Commands for
the user in fish syntax.
