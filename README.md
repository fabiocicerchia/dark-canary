# dark-canary

[![CI](https://github.com/fabiocicerchia/dark-canary/actions/workflows/code-quality.yml/badge.svg)](https://github.com/fabiocicerchia/dark-canary/actions/workflows/code-quality.yml)
[![Security](https://github.com/fabiocicerchia/dark-canary/actions/workflows/security.yml/badge.svg)](https://github.com/fabiocicerchia/dark-canary/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/fabiocicerchia/dark-canary/badge)](https://securityscorecards.dev/viewer/?uri=github.com/fabiocicerchia/dark-canary)


Mirror production traffic to a shadow deployment, compare the responses, and tell
the operator whether the shadow behaves identically. One path returns to the
user; the other is a dead end.

**The flagship.** The plumbing isn't the product — nginx's `mirror` directive
already does fire-and-forget subrequests. The product is the **diff engine plus
declarative noise suppression** for timestamps, IDs, ordering and float
precision. Diffy and Scientist did this for services and refactors; both are long
unmaintained, and nothing owns the edge-level version.

## Try it in one minute

```
make build
./bin/dark-canary -rules noise.example.json &

curl -X POST :8099/captures -d '{"path":"primary","correl_id":"a1","method":"GET",
  "status":200,"res_headers":{"Date":["Mon, 01 Jan 2035 00:00:00 GMT"]},
  "res_body":"<base64 of {\"total\":10.004,\"tags\":[\"a\",\"b\"],\"state\":\"paid\"}>"}'
curl -X POST :8099/captures -d '{...same, "path":"shadow", total 10.001, tags reversed, state "PAID"}'

curl :8099/report
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

## Status

| | |
| --- | --- |
| `internal/collector/` | bounded correlating buffer with a timeout | ✅ |
| `internal/diff/` | structural comparison — **the product** | ✅ |
| `internal/noise/` | declarative suppression rules | ✅ |
| `internal/safety/` | reads-only, sampling, PII scrub, kill switch | ✅ |
| `internal/report/` | grouped by frequency and severity | ✅ |
| `cmd/dark-canary/` | ingest, pipeline, `/report`, `/stats` | ✅ |
| `lua/dark_canary.lua` | capture hook over the WAF kit's mirror module | ✅ |
| Web UI | deliberately not built — see below | ⬜ |

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

```json
{ "path": "/body/**/updatedAt", "ignore": true,          "reason": "..." }
{ "path": "/body/total",        "normalise": "round:2",  "reason": "..." }
{ "path": "/body/**/tags",      "normalise": "sort",     "reason": "..." }
{ "path": "/body/**/token",     "normalise": "len",      "reason": "..." }
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

`noise.example.json` is a starting point. The built-in defaults are deliberately
short (Date, Server, request ids, Set-Cookie, Age, ETag) — a long default ruleset
hides real findings on day one, and a test fails if it grows past ten rules
without justification.

## Safety — designed in from day one, not bolted on

- **Reads only by default.** The edge refuses to mirror non-idempotent methods,
  *and* the collector refuses to accept a capture of one — so a misconfigured
  edge cannot quietly become shadow writes. Enabling `-allow-write-mirroring`
  logs a warning containing the words REAL WRITES.
- **Sampling** defaults to 1% at the edge, bounding load amplification on shared
  datastores.
- **PII scrubbing** happens at the edge before a capture is buffered, and again
  on arrival — because a capture can also come from a replay or an older edge.
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

## Edge wiring

`lua/dark_canary.lua` is deliberately thin — the kill switch, reads-only,
sampling, correlation and scrubbing all live in `nginx-lua-waf-kit`'s `mirror`
module, because that hook is useful on its own and building it there is what made
this project cheaper. What is here is labelling which side a capture came from
and shipping it. See that project's `examples/waf.conf.example`.

## Why there is no UI

The roadmap says build it last, and get one real service diffed end to end first.
The grouping the UI would render is built and headless: `/report` returns the same
summary as text or JSON, ranked by severity then frequency, so a rare 500 on the
shadow outranks a timestamp that differs on every request. If that is not useful
in a terminal, a web page will not rescue it.

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
