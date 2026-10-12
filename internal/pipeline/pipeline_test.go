package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLog struct {
	mu      sync.Mutex
	got     []Message
	fail    bool
	stalled chan struct{}
}

func (f *fakeLog) Send(m Message, done func(error)) {
	if f.stalled != nil {
		<-f.stalled
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		done(errors.New("broker unavailable"))
		return
	}
	f.got = append(f.got, m)
	done(nil)
}

func (f *fakeLog) Close(context.Context) error { return nil }

func TestPublisherDeliversInOrderAndCounts(t *testing.T) {
	log := &fakeLog{}
	p := NewPublisher(log, 16)
	for _, key := range []string{"a", "b", "c"} {
		p.Publish(Message{Topic: "orbit.elements", Key: []byte(key)})
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(log.got) != 3 || string(log.got[0].Key) != "a" || string(log.got[2].Key) != "c" {
		t.Fatalf("got %+v", log.got)
	}
	if c := p.Counters(); c != (Counters{Sent: 3}) {
		t.Fatalf("counters = %+v", c)
	}
}

func TestAStalledOrFailingLogNeverBlocksPublishing(t *testing.T) {
	stalled := &fakeLog{stalled: make(chan struct{})}
	p := NewPublisher(stalled, 4)
	finished := make(chan struct{})
	go func() {
		for range 5000 {
			p.Publish(Message{Topic: "t"})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked while the log was stalled")
	}
	if c := p.Counters(); c.Dropped < 4990 {
		t.Fatalf("counters = %+v, want almost everything dropped", c)
	}
	close(stalled.stalled)
	p.Close(context.Background())

	failing := NewPublisher(&fakeLog{fail: true}, 8)
	for range 5 {
		failing.Publish(Message{Topic: "t"})
	}
	failing.Close(context.Background())
	if c := failing.Counters(); c.Failed != 5 || c.Sent != 0 {
		t.Fatalf("counters = %+v, want 5 failed", c)
	}
}

func TestANilPublisherDiscardsEverything(t *testing.T) {
	var p *Publisher
	p.Publish(Message{Topic: "t"})
	if p.Counters() != (Counters{}) || p.Close(context.Background()) != nil {
		t.Fatal("a nil publisher should be a quiet no-op")
	}
}

func TestLoopKeepsGoingThroughErrorsAndPanics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int64
	var reported []string
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, 5*time.Millisecond, func(context.Context) error {
			switch runs.Add(1) {
			case 1:
				return errors.New("source is down")
			case 2:
				panic("bad element set")
			case 5:
				cancel()
			}
			return nil
		}, func(err error) {
			mu.Lock()
			reported = append(reported, err.Error())
			mu.Unlock()
		})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Loop did not stop after its context was cancelled")
	}
	if runs.Load() < 5 {
		t.Fatalf("only %d cycles ran; the loop stopped after a failure", runs.Load())
	}
	if len(reported) != 2 || reported[0] != "source is down" || reported[1] != "cycle panicked: bad element set" {
		t.Fatalf("reported = %q", reported)
	}
}

func TestLoopRunsACycleImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go Loop(ctx, time.Hour, func(context.Context) error {
		close(ran)
		return nil
	}, func(error) {})
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("the first cycle waited for the interval")
	}
	cancel()
}
