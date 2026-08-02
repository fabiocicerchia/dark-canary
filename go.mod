module github.com/fabiocicerchia/local-ai-lab/dark-canary

go 1.24

// Patched stdlib: go1.26.0 carries 12 govulncheck hits reachable from
// ListenAndServe (crypto/x509, crypto/tls, net/url, net/textproto, net, os).
toolchain go1.26.5
