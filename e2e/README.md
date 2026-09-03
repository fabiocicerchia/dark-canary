# End-to-end harness

Every component of dark-canary is tested in isolation. Nothing here had ever
mirrored one service against another end to end — which is the thing all the
other tests exist to make possible, and the one nobody had watched happen.

```sh
make e2e     # builds the binary, then runs the harness
```

## What it stands up

```
  load loop ──► dark-canary (proxy) ──► primary   ──► the user's answer
                      │
                      └──────────────► shadow    ──► discarded, captured
                      │
                      └──► collector ──► /report, /stats
```

Both services are the same code (`e2e/demo`) with different options: different
noise seeds, the shadow 5 ms slower, and the shadow carrying one deliberate
regression.

## Why the demo service is deliberately untidy

A synthetic load generator against a tidy demo service proves very little.
Tidy payloads are exactly the ones noise suppression does not need tuning for,
so the service differs between its two sides in the four ways a real pair of
deployments does:

- `trace_id`, `cursor` — unique per request by construction.
- `served_at`, `generated`, `duration_ms` — clocks and durations, never equal.
- `items` — collection order is not stable between two processes.
- `total`, `lines` — float addition is not associative, so summing the same
  numbers in a different order differs in the last place.

And underneath all of that, **one real bug**: on an order with a discount, the
shadow omits the `discount` field. The kind a finance team notices a week
later.

The whole design claim is that noise suppression makes that bug legible. The
harness asserts both directions — the bug is reported, and nothing else is. A
diff tool that reports the noise is worthless; one that suppresses the bug
along with it is worse.

Typical run:

```
pairs=381 identical=305 divergent=76 suppressed=1997
```

76 of 381 is the discounted-order rate. If that number approaches 381, the
noise rules have stopped working and the harness is passing by accident —
which is asserted too.

## What the run found

Three things, none of which a unit test would have surfaced. They are the
reason the harness is worth having:

- **`duration_ms` reached the report as a 94%-rate finding.** The first
  `e2e/noise.yaml` tried `normalise: len` on it, reasoning that a shadow an
  order of magnitude slower is worth knowing about. Wrong twice over: `len` is
  for strings and arrays, so on a number it does nothing — and the idea itself
  is wrong, because a duration in a response body measures the *server*, not
  its answer. It is `ignore` now, with that written down.
- **`-sample` defaults to 0.01.** Correct for production, and it meant the
  first run of this harness mirrored four requests in four hundred. The
  harness passes `-sample 1` explicitly.
- **The kill switch is eventually consistent, by one second.**
  `FileKillSwitch` caches its answer for a TTL so the hot path costs a clock
  read rather than a `stat` per request. A test asserting an *instant* stop
  found 120 captures still arriving — the documented contract working. The
  test waits the window out instead.

## What it asserts

- **the pair forms** — across two processes and a proxy, hundreds of times.
- **the real divergence is found** — and no noise is reported alongside it.
- **the user gets the primary's answer** — field by field, with the shadow
  both slow and wrong.
- **the kill file stops mirroring** — past its TTL, without costing one user
  request.
- **`-sample` samples** — 0.25 mirrors roughly a quarter, serves the rest.
- **unpaired captures expire** — 100 unpairable requests leave `pending` at 0.

## What it is not

**It is not real traffic.** The issue this closes asks for a real service
behind proxy mode with a genuine shadow, for a sustained period, and this is
not that: it runs for seconds, the payloads are shaped by hand, and the one
divergence is the one that was planted. What it gives is the shape of that run,
reproducibly and in CI, so a regression in the pairing, the suppression or the
safety path is caught before anyone points this at production.

For the sustained half, the demo service also runs standalone:

```sh
go run ./e2e/demo/cmd/demo-service -listen 127.0.0.1:9001 -name primary -seed 1
go run ./e2e/demo/cmd/demo-service -listen 127.0.0.1:9002 -name shadow \
    -seed 2 -bug -latency 15ms
./bin/dark-canary -primary http://127.0.0.1:9001 -shadow http://127.0.0.1:9002 \
    -rules e2e/noise.yaml -sample 1 -report-every 30s
```

Point a load generator at `127.0.0.1:8080` and leave it. Memory and buffer
growth show up in `/stats` (`pending`, `backlog`, `dropped`).

**`replicaCount > 1` is not exercised here and cannot be.** A pair only forms
inside one process, so captures split across pods never pair — the Helm chart
refuses it outright with a `fail`, which is the assertion that matters and it
lives in `charts/dark-canary/templates/deployment.yaml`.
