# dark-canary

[![CI](https://github.com/fabiocicerchia/dark-canary/actions/workflows/code-quality.yml/badge.svg)](https://github.com/fabiocicerchia/dark-canary/actions/workflows/code-quality.yml)
[![Security](https://github.com/fabiocicerchia/dark-canary/actions/workflows/security.yml/badge.svg)](https://github.com/fabiocicerchia/dark-canary/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/fabiocicerchia/dark-canary/badge)](https://securityscorecards.dev/viewer/?uri=github.com/fabiocicerchia/dark-canary)


Mirror production traffic to a shadow deployment, compare the responses, and tell
the operator whether the shadow behaves identically. One path returns to the
user; the other is a dead end.

**The flagship.** One binary sits in front of both deployments and does the
routing; the plumbing isn't the product, though — nginx's `mirror` directive
already does fire-and-forget subrequests. The product is the **diff engine plus
declarative noise suppression** for timestamps, IDs, ordering and float
precision. Diffy and Scientist did this for services and refactors; both are long
unmaintained, and nothing owns the edge-level version.

## Try it in one minute

Point it at two upstreams and send it traffic. No nginx, no Lua, no config file.

```
make build
./bin/dark-canary -rules noise.example.yaml \
  -primary http://127.0.0.1:9001 \
  -shadow  http://127.0.0.1:9002 \
  -proxy-listen 127.0.0.1:8080 -sample 1.0 &

curl 127.0.0.1:8080/orders/7      # served by the primary, mirrored to the shadow
curl 127.0.0.1:8099/report
```

```
1 pairs compared over 2s
0 identical (0.0% agreement), 1 divergent, 4 differences suppressed by noise rules

SEVERITY  COUNT  RATE    KIND        PATH         PRIMARY → SHADOW
low       1      100.0%  body_value  /body/state  paid → PAID
```

Four differences — the `Date` header, a timestamp, a float that agrees to the
cent, and a reordered array — suppressed by rules. One real behaviour change
surfaced. That contrast is the entire product.

## Two ways to run it

**Proxy mode** (`-primary` + `-shadow`) is the whole thing in one binary: it
routes the traffic itself. The client is answered by the primary; a copy of the
request is fired at the shadow and its response is read, compared and thrown
away. Nothing about the shadow — its latency, its errors, its being down — can
reach the client. Sampling, reads-only and the kill switch are enforced here, in
the process that does the routing.

A **dashboard** is served at `/` on the collector port: agreement rate, the
collector counters that answer "why is nothing being compared", and the ranked
divergence table. One embedded HTML file, no build step and no second container;
it only polls `/report` and `/stats`. When `-token` is set, open
`http://host:8099/#token=…` — the fragment is never sent in the request line, so
the token stays out of access logs.

**Collector mode** (`-listen` alone, the default) accepts captures over
`POST /captures` from an edge that already mirrors — nginx's `mirror` directive
plus `lua/dark_canary.lua`. Use it when the mirroring has to happen at an edge
you already run. `res_body` and `req_body` are base64: they are raw bytes.

Both modes feed the same buffer, diff engine and report. The safety controls
apply identically — there is no path into the buffer that skips scrubbing.

## Status

| | |
| --- | --- |
| `internal/collector/` | bounded correlating buffer with a timeout | ✅ |
| `internal/diff/` | structural comparison — **the product** | ✅ |
| `internal/noise/` | declarative suppression rules | ✅ |
| `internal/safety/` | reads-only, sampling, PII scrub, kill switch | ✅ |
| `internal/report/` | grouped by frequency and severity | ✅ |
| `cmd/dark-canary/` | ingest, pipeline, `/report`, `/stats` | ✅ |
| proxy mode | routes traffic itself: `-primary` / `-shadow` | ✅ |
| `lua/dark_canary.lua` | optional edge hook, needs nginx-lua-waf-kit | ⬜ |
| dashboard | one embedded HTML file on the collector port | ✅ |
| `Dockerfile`, `charts/` | scratch image (11MB) and a Helm chart | ✅ |

Not yet run against a real service end to end. That is the roadmap's gate
(one real service within six weeks of starting, or stop), and everything above
exists to make that possible rather than to substitute for it.

## The diff engine

Structural, never textual. A textual diff of two JSON responses reports 100%
divergence and is worthless.

- Object **key order** and whitespace are not differences.
- Numbers compare **numerically**: `1.0` and `1` are the same value, and large
  integers survive decoding (`json.Number`, not `float64`).
- An **array length** change is reported once, not as "every index after the
  insertion changed".
- Type changes (`"7"` → `7`) are their own kind, as are keys present on one side
  only — a client that expects that field will break, and that ranks above a
  changed value.
- Hop-by-hop and transport headers are excluded **without any rules**, so a fresh
  install is usable before anyone writes one.
- One side ceasing to be JSON is reported as exactly that, not as a byte diff.

Paths are one namespace — `/status`, `/headers/<name>`, `/body/<json pointer>` —
so a single glob syntax covers every rule. Keys containing `/` are escaped
(RFC 6901) and cannot forge a path a rule then matches by accident.

## Noise rules

Rules are data, loaded from a file, tuned without a redeploy:

```yaml
rules:
  - path: /body/**/updatedAt
    ignore: true
    reason: ...
  - path: /body/total
    normalise: round:2
    reason: ...
  - path: /body/**/tags
    normalise: sort
    reason: ...
  - path: /body/**/token
    normalise: len
    reason: ...
```

`*` matches one path segment, `**` any number. Later rules override earlier ones,
so an operator can re-enable something the defaults hid. `ignore` drops a
difference; `normalise` makes the narrower claim that the values must still
agree, just not exactly (`round:N`, `sort`, `trim`, `lower`, `len`).

The ruleset is consulted **during** comparison, not after — an array's ordering
and a float's precision can only be judged where both values still exist. Arrays
are offered whole before their elements are walked, which is what makes `sort`
possible at all.

**Every suppression is counted and reported.** Suppression must never be
mistakable for agreement, which is why the report leads with "N differences
suppressed by noise rules" and why a rule that neither ignores nor normalises is
rejected at load rather than silently doing nothing.

`noise.example.yaml` is a starting point. The built-in defaults are deliberately
short (Date, Server, request ids, Set-Cookie, Age, ETag) — a long default ruleset
hides real findings on day one, and a test fails if it grows past ten rules
without justification.

## Deploying it

```
docker build -t dark-canary .
docker run -p 8080:8080 -p 8099:8099 dark-canary \
  -listen 0.0.0.0:8099 -token "$TOKEN" \
  -primary http://primary:8080 -shadow http://shadow:8080

helm install dc charts/dark-canary \
  --set auth.token="$TOKEN" \
  --set proxy.primary=http://checkout.default.svc.cluster.local:8080 \
  --set proxy.shadow=http://checkout-shadow.default.svc.cluster.local:8080
```

Scratch image, no shell, non-root, read-only root filesystem, ~11MB. The chart
refuses to render rather than install something subtly useless: no token, a
`-primary` without a `-shadow`, or `replicaCount > 1` — a pair only forms inside
one process, so captures split across pods never pair at all.

In Kubernetes the kill switch is a key in the chart's ConfigMap, mounted at
`/etc/dark-canary/kill`: `--set safety.killSwitch=true`. That is the one control
that is *worse* here than on a VM — it propagates on the kubelet's sync rather
than the sub-second of a local `touch`. In a hurry, set `proxy.sample=0`.

## Safety — designed in from day one, not bolted on

- **Reads only by default.** The proxy refuses to mirror non-idempotent methods,
  *and* the collector refuses to accept a capture of one — so neither a bug here
  nor a misconfigured edge can quietly become shadow writes. Enabling `-allow-write-mirroring`
  logs a warning containing the words REAL WRITES.
- **Sampling** defaults to 1% (`-sample`), bounding load amplification on shared
  datastores. Mirrored requests in flight are bounded too (`-max-inflight`): a
  shadow that stops answering is not mirrored to, rather than queued behind.
- **PII scrubbing** happens on every path into the buffer, whether the capture
  was made by the proxy or arrived over the wire from a replay or an older edge.
  `Authorization`, `Cookie` and friends are redacted whether or not anyone
  configured them.
- **A kill switch that works when the control plane is down**: a file on local
  disk. `touch /etc/dark-canary/kill` over SSH and both the edge hook and the
  collector stop within a second. No API call, no config reload, no dependency on
  the thing that is probably also broken.
- Bodies are capped at the edge and again on arrival; truncation is recorded, not
  silent.
- **The collector binds `127.0.0.1` by default.** Captures carry production
  response bodies and `/report` serves them back, so a non-loopback `-listen`
  *requires* `-token`; the process refuses to start otherwise. The token is
  checked in constant time on `/captures`, `/report` and `/stats`.

## Back-pressure never reaches the edge

If the diff engine falls behind, pairs are **dropped and counted**, never queued
forever — blocking would push back onto the capture path, the one place this tool
must never affect. `/stats` accounts for every capture received: paired, pending,
expired (a partner that never arrived), dropped (buffer full), discarded
(malformed). "Why is nothing being compared" is the first question of any shadow
deployment, and that endpoint is the answer.

One process, one buffer: two captures only pair if they reach the same instance,
which is worth knowing before putting two replicas behind a load balancer.

## Edge wiring — only if you are not using proxy mode

Proxy mode needs none of this. Reach for the Lua hook when nginx is already in
the request path and a second hop is unacceptable; it requires
[`nginx-lua-waf-kit`](https://github.com/fabiocicerchia/nginx-lua-waf-kit), which
is a separate install.

`lua/dark_canary.lua` is deliberately thin — the kill switch, reads-only,
sampling, correlation and scrubbing all live in `nginx-lua-waf-kit`'s `mirror`
module, because that hook is useful on its own and building it there is what made
this project cheaper. What is here is labelling which side a capture came from
and shipping it. See that project's `examples/waf.conf.example`.

## Why the UI is one HTML file

There used to be no UI at all, on the grounds that the ranking is the product and
a web page cannot rescue a ranking that is not useful in a terminal. That still
holds, and it is what the dashboard is built out of rather than an argument
against it: the page has no server side, no state and no opinions. It polls
`/report` and `/stats` and renders exactly what they return, which is the same
summary the terminal gets — ranked by severity then frequency, so a rare 500 on
the shadow outranks a timestamp that differs on every request.

That constraint is the design:

- **No build step, no bundle, no CDN, no second container.** One `go:embed`'d
  file. A dashboard that needs its own deployment is a dashboard nobody installs,
  and the operator who needs it most is the one who has not installed it.
- **No analysis in the browser.** The only arithmetic on the page is the two
  percentages the text report derives from the same fields — agreement, and each
  group's rate. Grouping, ranking and severity all arrive decided. If the page
  and the terminal ever disagreed, the page would be wrong, so it is given
  nothing to be wrong about.
- **It is a view, never a control.** No buttons, no rule editing, no kill switch.
  Rules are files and the kill switch is a file, both deliberately reachable when
  the thing that is broken is the control plane; a web form in front of them
  would add a dependency on the process being healthy enough to serve it.
- **The counters are the point.** "Why is nothing being compared" is the first
  question of any shadow deployment, and received/pending/expired/dropped/
  discarded answer it before the divergence table is worth reading.

What is still deliberately absent: history, storage, alerting, and per-request
drill-down. Each needs a datastore, and this process is designed to be safe to
put in the request path — which means holding no production data longer than the
correlation window. Point `/report?format=json` at whatever already stores things.

## Tests

```
make test
```

Covers the diff engine's structural guarantees, noise rule matching and every
normalisation, collector correlation/expiry/bounding/back-pressure, the safety
controls, report grouping and ranking, and the HTTP surface end to end.

## Kill criterion

If you can't get one real service diffed end to end within 6 weeks of starting,
stop. See `../ROADMAP.md`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[GitHub Security Advisories](https://github.com/fabiocicerchia/dark-canary/security/advisories/new),
never a public issue — see [SECURITY.md](SECURITY.md).

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
