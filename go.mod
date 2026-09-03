module github.com/fabiocicerchia/dark-canary

go 1.24

// Patched stdlib: go1.26.0 carries 12 govulncheck hits reachable from
// ListenAndServe (crypto/x509, crypto/tls, net/url, net/textproto, net, os).
toolchain go1.26.8

require gopkg.in/yaml.v3 v3.0.1
