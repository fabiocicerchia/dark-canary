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

Record further significant choices here (or in a `docs/adr/` folder if they pile
up).
