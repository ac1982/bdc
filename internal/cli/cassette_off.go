//go:build !e2e

package cli

import "net/http"

// Release builds talk to the network directly; see cassette.go for the
// record/replay transport of end-to-end tests.
func cassetteTransport(real http.RoundTripper) http.RoundTripper { return real }

var stopCassette = func() {}
