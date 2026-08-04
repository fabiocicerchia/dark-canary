# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first: the module files change far less often than the code, so
# this layer survives most rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static, stripped, reproducible. CGO off is what makes scratch possible at all.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /dark-canary ./cmd/dark-canary

FROM scratch
# Only for -shadow/-primary over https; harmless otherwise.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /dark-canary /dark-canary

# Nothing in this image needs a shell, a package manager or root. There is no
# shell to get one with either.
USER 65532:65532

# 8080 proxied traffic, 8099 collector + dashboard.
EXPOSE 8080 8099

# Both listeners bind every interface here: the container *is* the boundary, and
# a loopback bind inside a container is reachable by nobody. -token is therefore
# mandatory in the chart whenever the collector port is exposed beyond the pod.
ENTRYPOINT ["/dark-canary"]
CMD ["-listen", "0.0.0.0:8099", "-proxy-listen", "0.0.0.0:8080"]
