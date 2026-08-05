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
