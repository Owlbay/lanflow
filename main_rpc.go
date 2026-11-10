package main

import (
	"context"
	"errors"
	"log"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/rpc"
	"lanflow/internal/view"
)

// startLocalAPI serves the backend on the local interface (internal/rpc,
// docs/service-split.md §3) for `lanflow ctl` and `lanflow mcp`: the
// socket <DataDir>/run/lanflow.sock (a per-user named pipe on Windows),
// the audit log in the log directory. The fake backend is served too
// (LANFLOW_FAKE=1), for trying agents without touching the real setup.
// prompt asks the user to allow an agent's writes. The returned function
// stops serving.
func startLocalAPI(api app.API, prompt rpc.PromptFunc, host rpc.Host) (stop func()) {
	srv := serveLocalAPI(api, prompt, host)
	if srv == nil {
		return func() {}
	}
	return func() { _ = srv.Close() }
}

// serveLocalAPI starts the server; nil when it cannot listen (logged).
func serveLocalAPI(api app.API, prompt rpc.PromptFunc, host rpc.Host) *rpc.Server {
	o := app.DefaultOptions("Lanflow")
	srv, err := rpc.Start(o.DataDir, rpc.Options{API: api, Version: mygo.App.Version(), AuditDir: o.LogDir, Prompt: prompt, Host: host})
	if err != nil {
		log.Printf("lanflow: local api: %v", err)
		return nil
	}
	return srv
}

// singleHost is the window ops of the single process: a second window
// process is sent to its window.
type singleHost struct{ shell *view.Shell }

func (singleHost) Split() bool             { return false }
func (h singleHost) ShowWindow(arg string) { h.shell.ShowWindow(); h.shell.Open(arg) }
func (singleHost) SetBackground(bool)      {}
func (singleHost) Quit()                   { mygo.App.Quit() }
func (h singleHost) SetShortcut(a, acc string) error {
	return errors.New("单进程模式下快捷键由窗口注册")
}

// agentPrompt asks the user in the window (view.Shell.AskAgentConsent)
// and waits for the answer; a request the caller stops waiting for is
// withdrawn from the window.
func agentPrompt(shell *view.Shell) rpc.PromptFunc {
	return func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		ch := make(chan string, 1)
		withdraw := shell.AskAgentConsent(view.AgentConsent{Client: c.Client, Agent: c.Agent, Kind: string(c.Kind), Summary: c.Summary},
			func(a string) { ch <- a })
		select {
		case a := <-ch:
			return rpc.Answer(a), nil
		case <-ctx.Done():
			withdraw()
			return "", ctx.Err()
		}
	}
}
