// demo-service runs one side of the e2e pair as a standalone process.
//
// The Go harness in e2e/ drives both sides in-process, which is what makes it
// runnable in CI. This binary exists for the other half of the issue: a
// SUSTAINED run, where you want the pair up for hours under a real load
// generator rather than for the twenty seconds a test can justify.
//
//	demo-service -listen 127.0.0.1:9001 -name primary
//	demo-service -listen 127.0.0.1:9002 -name shadow -bug -latency 15ms
//	dark-canary -primary http://127.0.0.1:9001 -shadow http://127.0.0.1:9002 \
//	    -rules e2e/noise.yaml -report-every 30s
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/fabiocicerchia/dark-canary/e2e/demo"
)

func main() {
	var (
		listen  = flag.String("listen", "127.0.0.1:9001", "address to serve on")
		name    = flag.String("name", "primary", "identifies this side in X-Served-By")
		bug     = flag.Bool("bug", false, "run the shadow's deliberate regression")
		seed    = flag.Int64("seed", 1, "noise seed; must DIFFER between the two sides")
		latency = flag.Duration("latency", 0, "added to every response")
	)
	flag.Parse()

	h := demo.Handler(demo.Options{
		Name: *name, Bug: *bug, Seed: *seed, Latency: *latency,
	})
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(os.Stderr, "demo-service %s on %s (bug=%v)\n", *name, *listen, *bug)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "demo-service:", err)
		os.Exit(1)
	}
}
