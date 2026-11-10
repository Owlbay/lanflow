package main

import (
	"log"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/rpc"
)

// startLocalAPI serves the backend on the local interface (internal/rpc,
// docs/service-split.md §3) for `lanflow ctl` and `lanflow mcp`: the
// socket <DataDir>/run/lanflow.sock (a per-user named pipe on Windows),
// the audit log in the log directory. The fake backend is served too
// (LANFLOW_FAKE=1), for trying agents without touching the real setup.
// prompt asks the user to allow an agent's writes. The returned function
// stops serving.
func startLocalAPI(api app.API, prompt rpc.PromptFunc) (stop func()) {
	o := app.DefaultOptions("Lanflow")
	srv, err := rpc.Start(o.DataDir, rpc.Options{API: api, Version: mygo.App.Version(), AuditDir: o.LogDir, Prompt: prompt})
	if err != nil {
		log.Printf("lanflow: local api: %v", err)
		return func() {}
	}
	return func() { _ = srv.Close() }
}
