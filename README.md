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

## Install

```sh
go install github.com/fabiocicerchia/dark-canary/cmd/dark-canary@latest
```

Or from a checkout:

```sh
make build      # -> ./bin/
```

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

## Documentation

Full docs live in [`docs/`](docs/). Runnable examples live in [`examples/`](examples/).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[GitHub Security Advisories](https://github.com/fabiocicerchia/dark-canary/security/advisories/new),
never a public issue — see [SECURITY.md](SECURITY.md).

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
