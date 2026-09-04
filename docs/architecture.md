# Architecture

## Overview

One process. Traffic goes in the proxy port, the primary answers the client, a
copy goes to the shadow and is discarded after being captured, and the two
captures are correlated, diffed and aggregated in memory.

```
                    ┌──────────────── dark-canary ────────────────┐
                    │                                             │
client ──:8088──────┼──► proxy ──────────────► primary ───────────┼──► response
                    │      │  └─ capture ─┐                       │    to client
                    │      └─► shadow ────┤ (discarded)           │
                    │                     ▼                       │
                    │              collector (correlate, bounded) │
                    │                     │ pairs                 │
                    │                     ▼                       │
                    │            diff engine ◄── noise rules      │
                    │                     │ differences           │
                    │                     ▼                       │
operator ──:8099────┼───► report aggregator ──► dashboard,        │
                    │                          /report, /stats    │
                    └─────────────────────────────────────────────┘
```

In collector mode the proxy is absent and captures arrive on `POST /captures`
from an edge that already mirrors. Everything downstream is identical.

## Components

| Package | Responsibility |
| --- | --- |
| `cmd/dark-canary/proxy.go` | proxy mode: routes traffic, mirrors, captures both sides |
| `cmd/dark-canary/config.go` | the flag surface, folded into the safety config, the ruleset and the upstreams |
| `cmd/dark-canary/exit.go` | which failure exits with which code |
| `cmd/dark-canary/dashboard.*` | the embedded HTML view; polls `/report` and `/stats` |
| `internal/collector` | correlates the two captures into a pair; bounded, with a timeout |
| `internal/diff` | structural comparison — **the product** |
| `internal/noise` | declarative suppression rules, consulted *during* comparison |
| `internal/safety` | reads-only, sampling, PII scrub, kill switch, body caps |
| `internal/report` | grouping and ranking by severity then frequency |
| `lua/dark_canary.lua` | optional edge hook for collector mode; needs `nginx-lua-waf-kit` |

## Data flow

1. **Decide, once.** `proxy.mirror()` runs the kill switch, reads-only, sampling
   and the in-flight bound *before* anything is forwarded. Deciding once is what
   stops the two captures disagreeing about whether a request is being compared —
   half a pair is worse than none, because it expires and skews the stats.
2. **Serve the primary.** `httputil.ReverseProxy` forwards and streams the
   response back. The capture is teed out of that stream rather than buffered, so
   streaming responses and large downloads still work.
3. **Mirror to the shadow.** A clone of the request — taken before the primary is
   served, since `ReverseProxy` owns the original from that point — on a detached
   context with its own timeout, in a goroutine holding one of `-max-inflight`
   slots. Its response is read, captured, and discarded.
4. **Ingest.** Both captures go through `server.ingest`, the one door into the
   buffer, so scrubbing and body caps apply on every path — including captures
   the process made itself.
5. **Correlate.** The collector holds unpaired captures until their partner
   arrives or `-correlate-timeout` expires.
6. **Diff and aggregate.** Each pair is compared structurally, with noise rules
   consulted as the walk happens, and folded into the running report.

## Decisions

**The diff engine is the product; the routing is not.** nginx's `mirror`
directive already does fire-and-forget subrequests. What nothing owned was
structural comparison plus declarative noise suppression at the edge.

**Nothing about the shadow may reach the client.** It is mirrored to in a
separate goroutine, on a detached context, with its own timeout, and its response
is discarded. Its latency, its errors and its being down are invisible to the
user by construction — that property is what makes the tool safe to put in the
request path at all.

**Back-pressure never reaches the edge.** If the diff engine falls behind, pairs
are dropped and counted, never queued. A shadow that stops answering fills the
in-flight slots and requests simply stop being mirrored. Blocking would push back
onto the capture path, the one place this tool must never affect.

**Rules are consulted during comparison, not after.** An array's ordering and a
float's precision can only be judged where both values still exist. Arrays are
offered whole before their elements are walked, which is what makes `sort`
possible at all.

**Every suppression is counted and reported.** Suppression must never be
mistakable for agreement. A rule that neither ignores nor normalises is rejected
at load rather than silently doing nothing.

**Safety is enforced at every door, not one.** Reads-only is checked in the proxy
*and* on `/captures`; scrubbing runs on every path into the buffer. A capture can
also arrive from a replay or an older edge, so no single chokepoint is trusted.

**The kill switch is a file.** No API call, no config reload, no dependency on
the control plane — often the thing that is also broken. It stops mirroring,
never serving.

**One process, one buffer.** Two captures only pair inside the same instance,
which is why the Helm chart refuses `replicaCount > 1` — split across pods,
nothing would ever pair. Sharding by correlation id would lift that, and is not
built.

**Nothing is stored.** No datastore, no history, no alerting. The process holds
production response bodies only for the correlation window, which is what keeps
it safe in the request path. Point `/report?format=json` at whatever already
stores things.

## Known gaps

- A dead or erroring primary produces no capture and no counter, so the dashboard
  cannot distinguish "the upstream is down" from "no traffic yet".
- A shadow that refuses the request leaves an orphan capture that expires; that
  is a finding the tool cannot express as a diff.
- Captures pair per-process only — see "one process, one buffer" above.
- The kill switch is eventually consistent, by its TTL (1 s by default): a
  burst arriving inside that window is still mirrored. `FileKillSwitch` caches
  its answer so the hot path costs a clock read rather than a `stat` per
  request, and "everything stops within TTL" is the contract. The e2e harness
  waits the window out rather than asserting an instant stop.
- No run against real traffic. `make e2e` mirrors a real service against a real
  shadow with the real binary between them, but the payloads are shaped by hand
  and the one divergence is the one that was planted.

Record further significant choices here (or in a `docs/adr/` folder if they pile
up).

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
make test      # components, in isolation
make e2e       # the whole thing, against two real services
```

`make test` covers the diff engine's structural guarantees, noise rule matching
and every normalisation, collector correlation/expiry/bounding/back-pressure,
the safety controls, report grouping and ranking, and the HTTP surface end to
end.

`make e2e` covers the thing all of those exist to make possible, and which none
of them touches: a real primary, a real shadow, the real binary proxying
between them, and a load loop through it. The demo service is deliberately
untidy — ids, clocks, unstable collection order, non-associative float sums —
with one real regression underneath, and the harness asserts that the
regression is reported **and that nothing else is**. A diff tool that reports
the noise is worthless; one that suppresses the bug along with it is worse, and
only a run like this can tell the two apart. See
[`e2e/README.md`](https://github.com/fabiocicerchia/dark-canary/blob/main/e2e/README.md), including the three things the first run
of it found.

It is not real traffic, and the README says so. It gives the shape of that run
reproducibly and in CI; the sustained version is the same demo service run
standalone with a load generator pointed at it.
