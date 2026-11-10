package remote_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/remote"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
	"lanflow/internal/rpc/rpctest"
)

func dialWindow(t *testing.T, s *rpctest.Server) *remote.Client {
	t.Helper()
	c, err := remote.Dial(context.Background(), remote.Options{DataDir: s.Dir, Client: rpc.KindUI, Version: "test", Cache: true, Incremental: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// waitEvent waits until the client has seen a "changed" event of each
// topic after fn ran (events of one change travel separately).
func waitEvent(t *testing.T, c *remote.Client, fn func(), topics ...app.Topic) {
	t.Helper()
	ch := make(chan app.Topic, 64)
	off := c.OnChange(func(tp app.Topic) { ch <- tp })
	defer off()
	fn()
	want := map[app.Topic]bool{}
	for _, tp := range topics {
		want[tp] = true
	}
	for len(want) > 0 {
		select {
		case tp := <-ch:
			delete(want, tp)
		case <-time.After(5 * time.Second):
			t.Fatalf("no %v event", want)
		}
	}
}

// The window's copies of the large lists match the service's full lists
// after every kind of change, and reads between events cost no call.
func TestIncrementalMirror(t *testing.T) {
	s := rpctest.Start(t, nil)
	f := s.Fake
	c := dialWindow(t, s)
	c.OnChange(func(app.Topic) {})

	check := func(step string) {
		t.Helper()
		if got, want := c.Logs(), f.Logs(); !sameLogs(got, want) {
			t.Fatalf("%s: logs %d entries, want %d", step, len(got), len(want))
		}
		if got, want := c.AppLogs(), f.AppLogs(); len(got) != len(want) {
			t.Fatalf("%s: app logs %d, want %d", step, len(got), len(want))
		}
		got, want := c.Connections(), f.Connections()
		if len(got) != len(want) {
			t.Fatalf("%s: %d connections, want %d", step, len(got), len(want))
		}
		for i := range got {
			if !model.SameConnection(got[i], want[i]) || got[i].ID != want[i].ID {
				t.Fatalf("%s: connection %d = %+v, want %+v", step, i, got[i], want[i])
			}
		}
		gotC, wantC := c.ClosedConnections(), f.ClosedConnections()
		if len(gotC) != len(wantC) {
			t.Fatalf("%s: %d closed, want %d", step, len(gotC), len(wantC))
		}
		for i := range gotC {
			if gotC[i].ID != wantC[i].ID {
				t.Fatalf("%s: closed order %v", step, gotC)
			}
		}
	}
	check("first read")
	for i := range 5 {
		waitEvent(t, c, f.Tick, app.TopicConns, app.TopicLogs)
		check("tick " + string(rune('0'+i)))
	}
	ids := f.Connections()
	waitEvent(t, c, func() { _ = f.CloseConnection(ids[0].ID) }, app.TopicConns)
	check("one closed")
	waitEvent(t, c, f.ClearLogs, app.TopicLogs)
	check("logs cleared")
	waitEvent(t, c, f.ClearAppLogs, app.TopicLogs)
	check("app logs cleared")
	waitEvent(t, c, func() { _ = f.CloseAllConnections() }, app.TopicConns)
	check("all closed")

	// A copy handed out is the caller's: sorting it changes nothing held.
	l := c.ClosedConnections()
	slices.Reverse(l)
	if got := c.ClosedConnections(); len(got) > 1 && got[0].ID == l[0].ID {
		t.Fatal("cached list shared with the caller")
	}
}

// Reads are served from the cache until an event of their topic.
func TestReadCache(t *testing.T) {
	s := rpctest.Start(t, nil)
	c := dialWindow(t, s)
	c.OnChange(func(app.Topic) {})
	before := c.Settings().Mode
	// Change behind the client's back without an event the cache sees:
	// the old value stays until the settings event arrives.
	waitEvent(t, c, func() {
		set := s.Fake.Settings()
		set.Mode = model.ModeGlobal
		_ = s.Fake.SaveSettings(set)
	}, app.TopicSettings)
	if got := c.Settings().Mode; got != model.ModeGlobal || before == got {
		t.Fatalf("mode = %s (before %s)", got, before)
	}
	// An action through the client drops the cache at once.
	if err := c.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Mode; got != model.ModeDirect {
		t.Fatalf("status after action = %s", got)
	}
}

func sameLogs(a, b []model.LogEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Time.Equal(b[i].Time) || a[i].Level != b[i].Level || a[i].Payload != b[i].Payload {
			return false
		}
	}
	return true
}

// A "changed" handler may call the API (the window takes the notices
// when TopicNotice arrives): events must not be delivered on the
// goroutine that reads the replies.
func TestHandlerMayCall(t *testing.T) {
	s := rpctest.Start(t, nil)
	c := dialWindow(t, s)
	c.Timeout = 5 * time.Second
	got := make(chan int, 4)
	c.OnChange(func(tp app.Topic) {
		if tp == app.TopicNotice {
			got <- len(c.TakeNotices())
		}
	})
	s.Fake.PushNotice(app.Notice{Kind: "toast", Title: "hi"})
	select {
	case n := <-got:
		if n != 1 {
			t.Fatalf("took %d notices", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's call never returned: deadlock on the reading goroutine")
	}
	// The connection still works afterwards.
	if c.Status().Core == "" {
		t.Fatalf("status after the handler: %v", c.LastError())
	}
}
