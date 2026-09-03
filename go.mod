module github.com/fabiocicerchia/dark-canary

go 1.24

// Patched stdlib: go1.26.0 carries 12 govulncheck hits reachable from
// ListenAndServe (crypto/x509, crypto/tls, net/url, net/textproto, net, os).
// The five this branch bumped for are ones this code does call — the proxy's
// own http.Client.Do and ReverseProxy.RoundTrip reach them, so they were not
// theoretical here (GO-2026-5026 among them, in net/http via x/net/idna).
// main has since moved past that, so take the higher toolchain.
toolchain go1.26.8

require gopkg.in/yaml.v3 v3.0.1
