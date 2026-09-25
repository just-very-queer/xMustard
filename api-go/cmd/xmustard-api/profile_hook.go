//go:build profile

package main

// Diagnostic-only profiling hook, compiled only with `go build -tags profile`.
// Default binaries import no net/http/pprof and serve no profile handler. With the
// tag, XMUSTARD_PPROF_ADDR=127.0.0.1:<port> starts a separate loopback-only listener;
// any non-loopback address is refused. Instrumented binaries are never gate binaries.

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
)

func init() {
	addr := os.Getenv("XMUSTARD_PPROF_ADDR")
	if addr == "" {
		return
	}
	if !loopbackAddr(addr) {
		log.Printf("profile: refusing non-loopback XMUSTARD_PPROF_ADDR %q", addr)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/xm/stats", func(w http.ResponseWriter, _ *http.Request) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rusage": childHighWater(), "num_gc": ms.NumGC, "pause_total_ns": ms.PauseTotalNs,
			"gc_cpu_fraction": ms.GCCPUFraction, "total_alloc": ms.TotalAlloc, "mallocs": ms.Mallocs,
			"heap_alloc": ms.HeapAlloc, "heap_inuse": ms.HeapInuse, "heap_idle": ms.HeapIdle,
			"heap_released": ms.HeapReleased, "heap_sys": ms.HeapSys, "sys": ms.Sys, "next_gc": ms.NextGC,
		})
	})
	go func() { log.Printf("profile: listener ended: %v", http.ListenAndServe(addr, mux)) }()
}

func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
