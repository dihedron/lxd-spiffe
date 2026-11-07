//go:build lab

package startup

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: a lab build only, on a loopback address
	"os"
	"time"
)

// A lab build (-tags lab, lab stress spec ST-8) serves Go's pprof on the
// loopback address that <BINARY>_PPROF_ADDR names; release builds do not
// contain this file.
func init() { debug = servePprof }

func servePprof(name string) {
	addr := os.Getenv(variable(name, "PPROF_ADDR"))
	if addr == "" {
		return
	}
	if err := loopback(addr); err != nil {
		slog.Error("not serving pprof", "address", addr, "error", err)
		return
	}
	srv := &http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("pprof server stopped", "address", addr, "error", err)
		}
	}()
}

// loopback accepts only host:port addresses on a loopback address.
func loopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("not a loopback address")
	}
	return nil
}
