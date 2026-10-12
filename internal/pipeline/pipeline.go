// Package pipeline keeps the published results fresh: it repeats a cycle
// (refresh the orbit data, screen it, write the viewer's files) on a
// schedule, and announces what changed on a message log such as Kafka.
//
// Two rules keep it running when things around it fail. A cycle that fails
// is reported and tried again at the next tick; the loop never exits because
// of one. And the message log is optional at every moment: publishing never
// blocks a cycle, and a broker that is slow, down or was never configured
// costs messages, not results.
package pipeline

import (
	"context"
	"sync/atomic"
	"time"
)

// Message is one record for the log.
type Message struct {
	Topic string
	Key   []byte
	Value []byte
}

// Log is where messages go. Send must not block; it reports failure
// through done, possibly later and from another goroutine.
type Log interface {
	Send(m Message, done func(error))
	Close(ctx context.Context) error
}

// Counters says what happened to the messages so far.
type Counters struct {
	Sent    uint64 `json:"sent"`
	Failed  uint64 `json:"failed"`
	Dropped uint64 `json:"dropped"` // the queue was full
}

// Publisher is a bounded, non-blocking queue in front of a Log. A nil
// *Publisher is valid and discards everything, which is how the pipeline
// runs when no log is configured.
type Publisher struct {
	log   Log
	queue chan Message
	done  chan struct{}

	sent, failed, dropped atomic.Uint64
}

// NewPublisher starts a publisher that holds at most buffer messages.
func NewPublisher(log Log, buffer int) *Publisher {
	p := &Publisher{log: log, queue: make(chan Message, buffer), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for m := range p.queue {
			p.log.Send(m, func(err error) {
				if err != nil {
					p.failed.Add(1)
				} else {
					p.sent.Add(1)
				}
			})
		}
	}()
	return p
}

// Publish queues a message, or counts it as dropped if the queue is full.
func (p *Publisher) Publish(m Message) {
	if p == nil {
		return
	}
	select {
	case p.queue <- m:
	default:
		p.dropped.Add(1)
	}
}

// Counters returns a snapshot of the counters.
func (p *Publisher) Counters() Counters {
	if p == nil {
		return Counters{}
	}
	return Counters{Sent: p.sent.Load(), Failed: p.failed.Load(), Dropped: p.dropped.Load()}
}

// Close sends what is queued, for as long as ctx allows. Publish must not
// be called after Close.
func (p *Publisher) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	close(p.queue)
	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.log.Close(ctx)
}

// Loop runs cycle now and then every interval until ctx is cancelled. A
// failed cycle is passed to report and does not stop the loop. A cycle that
// overruns the interval delays the next one; cycles never overlap.
func Loop(ctx context.Context, interval time.Duration, cycle func(context.Context) error, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := runCycle(ctx, cycle); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runCycle turns a panic inside a cycle into an error, so a bug triggered
// by one odd input cannot take the whole service down.
func runCycle(ctx context.Context, cycle func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = panicError{v}
		}
	}()
	return cycle(ctx)
}

type panicError struct{ value any }

func (p panicError) Error() string { return "cycle panicked: " + toString(p.value) }

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "unexpected failure"
}
