package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func slowRunner(t *testing.T, delay time.Duration) *Runner {
	t.Helper()
	var mu sync.Mutex
	started := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		first := !started
		started = true
		mu.Unlock()
		if first {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(textAnswer("done after the wait")))
	}))
	t.Cleanup(srv.Close)
	r, _ := runner(t, &scripted{answers: []string{textAnswer("x")}})
	client, err := NewClient(ClientConfig{Key: "k", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	r.Client = client
	return r
}

func drain(t *testing.T, run *Run) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var all []Event
	for {
		events, done := run.Next(ctx, len(all))
		all = append(all, events...)
		if done || ctx.Err() != nil {
			return all
		}
	}
}

func TestARunOutlivesTheRequestThatStartedIt(t *testing.T) {
	r := slowRunner(t, 300*time.Millisecond)
	c, _ := r.Store.Create("octocat")
	runs := NewRuns()
	unlocked := make(chan struct{})
	run, started := runs.Start(c, Input{Text: "go"}, r, func() { close(unlocked) })
	if !started || !runs.Active(c.ID) {
		t.Fatal("the run must start and be active")
	}
	if _, again := runs.Start(c, Input{Text: "again"}, r, func() {}); again {
		t.Error("a second turn on a running chat must be refused")
	}

	events := drain(t, run)
	last := events[len(events)-1]
	if last.Event != "done" {
		t.Fatalf("events = %+v", events)
	}
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		t.Fatal("the run must release the chat when it ends")
	}
	saved, _ := r.Store.Load("octocat", c.ID)
	if len(saved.Messages) != 2 || !strings.Contains(saved.Messages[1].Content, "done after the wait") {
		t.Errorf("the chat on disk must carry the finished turn: %+v", saved.Messages)
	}
	if runs.Active(c.ID) {
		t.Error("a finished run is not active")
	}
}

func TestALateSubscriberReplaysTheBufferThenFollowsLive(t *testing.T) {
	r := slowRunner(t, 200*time.Millisecond)
	c, _ := r.Store.Create("octocat")
	runs := NewRuns()
	run, _ := runs.Start(c, Input{Text: "go"}, r, func() {})
	time.Sleep(400 * time.Millisecond)
	first := drain(t, run)
	kinds := []string{}
	for _, e := range first {
		kinds = append(kinds, e.Event)
	}
	if strings.Join(kinds, " ") != "assistant done" {
		t.Errorf("replay = %v", kinds)
	}
	again, ok := runs.Get(c.ID)
	if !ok || again != run {
		t.Error("a finished run stays reachable for a while")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if events, done := run.Next(ctx, len(first)); len(events) != 0 || !done {
		t.Errorf("after the end Next answers empty and done, got %v %v", events, done)
	}
}

func TestFinishedRunsAreSweptAfterRetention(t *testing.T) {
	runs := NewRuns()
	r := newRun("x")
	r.emit(Event{Event: "done"})
	runs.runs["x"] = r
	runs.now = func() time.Time { return time.Now().Add(runRetention + time.Second) }
	if _, ok := runs.Get("x"); ok {
		t.Error("an old finished run must be swept")
	}
}
