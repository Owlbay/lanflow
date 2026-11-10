package main

import (
	"log"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on the default mux
	"os"
	"runtime/debug"
)

// Profiling and the Go runtime's memory behaviour.
//
// LANFLOW_PPROF=127.0.0.1:6060 serves net/http/pprof (CPU, heap, goroutine
// and execution-trace profiles) on that address for the life of the
// process; it is never on by default and only meant for local diagnosis:
//
//	LANFLOW_PPROF=127.0.0.1:6060 LANFLOW_FAKE=1 go run .
//	go tool pprof -top http://127.0.0.1:6060/debug/pprof/profile?seconds=20
//
// The soft memory limit and the GC target keep the heap small while the app
// idles for days in the tray: the runtime collects more eagerly as the heap
// nears softMemoryLimit and gives freed spans back to the OS, instead of
// letting a 40 MB live heap grow to 80 MB before the next collection. The
// limit is soft: the app keeps working above it, the collector only works
// harder. LANFLOW_GOMEMLIMIT=off keeps the runtime's defaults, for A/B
// measurements.
const softMemoryLimit = 96 << 20

func init() {
	if addr := os.Getenv("LANFLOW_PPROF"); addr != "" {
		go func() {
			if err := http.ListenAndServe(addr, nil); err != nil {
				log.Printf("lanflow: pprof: %v", err)
			}
		}()
	}
	if os.Getenv("LANFLOW_GOMEMLIMIT") == "off" || os.Getenv("GOMEMLIMIT") != "" || os.Getenv("GOGC") != "" {
		return // the environment rules
	}
	debug.SetMemoryLimit(softMemoryLimit)
	// Half the default growth between collections: the UI allocates
	// short-lived garbage every frame, which a smaller target returns
	// sooner; the extra GC work is negligible beside drawing.
	debug.SetGCPercent(50)
}
