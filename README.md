# dark-canary

[![CI](https://github.com/fabiocicerchia/dark-canary/actions/workflows/code-quality.yml/badge.svg)](https://github.com/fabiocicerchia/dark-canary/actions/workflows/code-quality.yml)
[![Security](https://github.com/fabiocicerchia/dark-canary/actions/workflows/security.yml/badge.svg)](https://github.com/fabiocicerchia/dark-canary/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/fabiocicerchia/dark-canary/badge)](https://securityscorecards.dev/viewer/?uri=github.com/fabiocicerchia/dark-canary)
[![CI carbon](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/fabiocicerchia/dark-canary/gh-pages/badge.json)](.github/workflows/carbon-badge.yml)

Mirror production traffic to a shadow deployment, compare the responses, and tell
the operator whether the shadow behaves identically. One path returns to the
user; the other is a dead end.

**The flagship.** One binary sits in front of both deployments and does the
routing; the plumbing isn't the product, though — nginx's `mirror` directive
already does fire-and-forget subrequests. The product is the **diff engine plus
declarative noise suppression** for timestamps, IDs, ordering and float
precision. Diffy and Scientist did this for services and refactors; both are long
unmaintained, and nothing owns the edge-level version.

## Features

- Mirrors production traffic to a shadow deployment and **structurally diffs
  the responses** — one path returns to the user, the other is a dead end.
- **Declarative noise suppression** is the product: timestamps, IDs, ordering
  and float precision are ruled out by config, so what is left is real
  behaviour change.
- Reports by severity with rates and paths, and counts what the rules
  suppressed — the contrast between "4 differences suppressed" and "1
  divergent" is the whole point.
- One binary in front of both upstreams: no nginx, no Lua, no config file to
  get started.
- `-sample` controls what fraction of traffic is mirrored, so it can be turned
  on against real load gradually.
- Separate report endpoint, so the comparison can be read while traffic keeps
  flowing.
- Ships as Homebrew, `.deb`, `.rpm`, `.apk` and Arch packages, via `go
  install`, or built from a checkout.

## Install

macOS, via Homebrew:

```sh
brew install fabiocicerchia/tap/dark-canary
```

Linux — a `.deb`, `.rpm`, `.apk` or Arch package from the
[latest release](https://github.com/fabiocicerchia/dark-canary/releases/latest):

```sh
sudo dpkg -i dark-canary_*_linux_amd64.deb     # or rpm -i / apk add --allow-untrusted
```

Or with Go:

```sh
go install github.com/fabiocicerchia/dark-canary/cmd/dark-canary@latest
```

Or from a checkout:

```sh
make build      # -> ./bin/
```

## Try it in one minute

Point it at two upstreams and send it traffic. No nginx, no Lua, no config file.

```bash
make build
./bin/dark-canary -rules noise.example.yaml \
  -primary http://127.0.0.1:9001 \
  -shadow  http://127.0.0.1:9002 \
  -proxy-listen 127.0.0.1:8080 -sample 1.0 &

curl 127.0.0.1:8080/orders/7      # served by the primary, mirrored to the shadow
curl 127.0.0.1:8099/report
```

```text
1 pairs compared over 2s
0 identical (0.0% agreement), 1 divergent, 4 differences suppressed by noise rules

SEVERITY  COUNT  RATE    KIND        PATH         PRIMARY → SHADOW
low       1      100.0%  body_value  /body/state  paid → PAID
```

Four differences — the `Date` header, a timestamp, a float that agrees to the
cent, and a reordered array — suppressed by rules. One real behaviour change
surfaced. That contrast is the entire product.

## Verify the download

Every release is signed with [cosign][cosign], keyless: the identity is the
workflow that published it, not a key anybody holds.

```sh
cosign verify-blob \
  --bundle checksums.txt.bundle \
  --certificate-identity-regexp 'https://github.com/fabiocicerchia/dark-canary' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

[cosign]: https://docs.sigstore.dev/

## Documentation

Full docs live in [`docs/`](docs/). Runnable examples live in [`examples/`](examples/).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[GitHub Security Advisories](https://github.com/fabiocicerchia/dark-canary/security/advisories/new),
never a public issue — see [SECURITY.md](SECURITY.md).

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
