package chat

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	RunTimeout   = 10 * time.Minute
	runRetention = 2 * time.Minute
)

type Run struct {
	ChatID string

	mu      sync.Mutex
	cond    *sync.Cond
	events  []Event
	done    bool
	started time.Time
	ended   time.Time
}

func newRun(chatID string) *Run {
	r := &Run{ChatID: chatID, started: time.Now()}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *Run) emit(e Event) error {
	r.mu.Lock()
	r.events = append(r.events, e)
	if e.Event == "done" || e.Event == "error" {
		r.done = true
		r.ended = time.Now()
	}
	r.cond.Broadcast()
	r.mu.Unlock()
	return nil
}

func (r *Run) finish() {
	r.mu.Lock()
	if !r.done {
		r.done = true
		r.ended = time.Now()
		r.cond.Broadcast()
	}
	r.mu.Unlock()
}

func (r *Run) Done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

func (r *Run) Next(ctx context.Context, from int) ([]Event, bool) {
	stop := context.AfterFunc(ctx, func() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	})
	defer stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.events) <= from && !r.done && ctx.Err() == nil {
		r.cond.Wait()
	}
	if len(r.events) > from {
		return append([]Event(nil), r.events[from:]...), r.done
	}
	return nil, r.done
}

type Runs struct {
	mu   sync.Mutex
	runs map[string]*Run
	now  func() time.Time
}

func NewRuns() *Runs {
	return &Runs{runs: map[string]*Run{}, now: time.Now}
}

func (rs *Runs) Active(chatID string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r, ok := rs.runs[chatID]
	return ok && !r.Done()
}

func (rs *Runs) Get(chatID string) (*Run, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.sweep()
	r, ok := rs.runs[chatID]
	return r, ok
}

func (rs *Runs) Start(c *Chat, in Input, runner *Runner, unlock func()) (*Run, bool) {
	rs.mu.Lock()
	rs.sweep()
	if r, ok := rs.runs[c.ID]; ok && !r.Done() {
		rs.mu.Unlock()
		return r, false
	}
	r := newRun(c.ID)
	rs.runs[c.ID] = r
	rs.mu.Unlock()

	go func() {
		defer unlock()
		defer r.finish()
		ctx, cancel := context.WithTimeout(context.Background(), RunTimeout)
		defer cancel()
		if err := runner.Run(ctx, c, in, r.emit); err != nil {
			log.Printf("chat: %s turn: %v", c.ID, err)
			r.emit(Event{Event: "error", Error: err.Error()})
		}
	}()
	return r, true
}

func (rs *Runs) sweep() {
	cutoff := rs.now().Add(-runRetention)
	for id, r := range rs.runs {
		r.mu.Lock()
		stale := r.done && r.ended.Before(cutoff)
		r.mu.Unlock()
		if stale {
			delete(rs.runs, id)
		}
	}
}
