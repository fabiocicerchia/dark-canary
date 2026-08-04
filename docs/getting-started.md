# Getting Started

## Prerequisites

- Go 1.24+ (only to build; the binary has one dependency, `gopkg.in/yaml.v3`)
- Two HTTP services to compare — a primary and a shadow

Nothing else. No nginx, no Lua, no datastore.

## Build

```sh
make build          # → ./bin/dark-canary
```

## Run

```sh
./bin/dark-canary -rules noise.example.yaml \
  -primary http://127.0.0.1:9001 \
  -shadow  http://127.0.0.1:9002 \
  -proxy-listen 127.0.0.1:8088 -sample 1.0
```

Two listeners come up:

| Port | What |
| --- | --- |
| `8088` (`-proxy-listen`) | your traffic — send requests here instead of to the primary |
| `8099` (`-listen`) | dashboard at `/`, plus `/report`, `/stats`, `/captures`, `/healthz` |

Send it traffic and open the dashboard:

```sh
curl 127.0.0.1:8088/orders/7      # answered by the primary, byte for byte
open http://127.0.0.1:8099/
```

A request through the proxy looks exactly like one to the primary — that is the
point. Everything interesting is on `8099`.

`-sample 1.0` mirrors everything, which is what you want for a first run. The
default is `0.01` — 1% — because mirroring amplifies load on whatever the shadow
shares with production.

## Reading the report

```
5 pairs compared over 2s
0 identical (0.0% agreement), 5 divergent, 20 differences suppressed by noise rules

SEVERITY  COUNT  RATE    KIND        PATH         PRIMARY → SHADOW
low       5      100.0%  body_value  /body/state  paid → PAID
```

- **Agreement counts pairs, not differences.** A pair is identical or it is not,
  so one difference on every request reads as 0% agreement even when that
  difference is a single low-severity one. The number is deliberately
  unforgiving; the table tells you whether to care.
- **"Suppressed" is not "agreed".** 20 differences were seen and matched a rule.
  That count leads the report so suppression can never be mistaken for agreement
  — if it climbs while divergences stay flat, the rules are hiding things.
- **Severity ranks the kind, not the risk.** A key present on one side only
  outranks a changed value, because a client expecting that field breaks
  outright. `low` does not mean safe: `paid → PAID` is ranked low and still
  breaks `state === "paid"`.

The first instinct on seeing a row is to write a rule that hides it. Only do that
when the values differ *by construction* — a timestamp, a request id, an
unordered array, a float summed in a different order. That is what `reason:` is
for; a rule nobody can explain is a rule nobody dares delete. See
[noise rules](../README.md#noise-rules).

## When nothing is being compared

`/stats` accounts for every capture, and is the first thing to look at:

```sh
curl 127.0.0.1:8099/stats
```

| Symptom | Cause |
| --- | --- |
| `curl` gets **502**, all counters `0` | the primary is down or `-primary` is wrong — no response existed to capture |
| all counters `0`, no 502 | nothing is being mirrored: check `-sample`, whether the method is idempotent under reads-only, and whether the kill file exists |
| `received` climbs, `paired` stays `0` | only one side is arriving; in collector mode, the two captures carry different `correl_id`s |
| `expired` climbs | a partner never arrived within `-correlate-timeout` |
| `dropped` climbs | the diff engine is behind and the buffer is full — raise `-max-pending` |
| `discarded` climbs | malformed captures: no correlation id, or an unknown `path` |
| `bind: address already in use` | an earlier instance is still running — `pkill -f bin/dark-canary` |

A **502 with every counter at zero is the common first-run case**: the upstreams
are not up. Two throwaway ones, if you just want to watch it work:

```sh
python3 -c 'import http.server as h;h.HTTPServer(("127.0.0.1",9001),type("H",(h.BaseHTTPRequestHandler,),{"do_GET":lambda s:(s.send_response(200),s.send_header("Content-Type","application/json"),s.end_headers(),s.wfile.write(b"{\"total\":10.004,\"tags\":[\"a\",\"b\"],\"state\":\"paid\",\"updatedAt\":\"2035-01-01T00:00:00Z\"}"))})).serve_forever()' &
python3 -c 'import http.server as h;h.HTTPServer(("127.0.0.1",9002),type("H",(h.BaseHTTPRequestHandler,),{"do_GET":lambda s:(s.send_response(200),s.send_header("Content-Type","application/json"),s.end_headers(),s.wfile.write(b"{\"total\":10.001,\"tags\":[\"b\",\"a\"],\"state\":\"PAID\",\"updatedAt\":\"2035-06-30T12:00:00Z\"}"))})).serve_forever()' &
```

That pair gives you four suppressed differences and one real one — the contrast
the tool exists to produce.

**Known gap:** a dead primary is invisible on the dashboard. The client sees the
502, but no capture is made, so the counters stay at zero and look identical to
"no traffic yet". Proxy-level error counters are not implemented.

## Before pointing it at production

- Leave `-sample` low. 1% is the default for a reason.
- Leave reads-only on. `-allow-write-mirroring` means the shadow does **real
  writes**, and the startup warning says so in capitals.
- Set `-scrub` for any body field carrying PII. `Authorization`, `Cookie`,
  `Set-Cookie` and `X-Api-Key` are redacted whether or not you configure them.
- Know where the kill file is (`-kill-file`, default `/etc/dark-canary/kill`).
  `touch` it and mirroring stops within a second; traffic keeps being served.
- Bind `-listen` to loopback, or set `-token`. The process refuses to start
  exposed and unauthenticated, because `/report` serves production response
  bodies back.

## Deploying

See [the README](../README.md#deploying-it) for the Docker image and Helm chart.
