//go:build pprof

// Profiling build (go build -tags pprof): when ASTERISM_AGENT_PPROF is set
// to a loopback address such as 127.0.0.1:6060, the agent serves
// net/http/pprof there. Release builds do not contain this file, so the
// agent never listens and links no net/http/pprof (BLUEPRINT D22).
package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
)

func init() {
	addr := os.Getenv("ASTERISM_AGENT_PPROF")
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		fmt.Fprintf(os.Stderr, "asterism-agent: ASTERISM_AGENT_PPROF must be a loopback host:port, got %q\n", addr)
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() { _ = http.ListenAndServe(addr, mux) }()
	fmt.Fprintf(os.Stderr, "asterism-agent: profiling build, pprof on http://%s/debug/pprof/\n", addr)
}
