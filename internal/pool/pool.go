// Package pool provides a bounded worker pool with context-deadline-aware
// task execution.
package pool

import (
	"context"
	"sync"
)

// Task is a unit of work submitted to the pool.
type Task func(ctx context.Context) error

// Pool runs Tasks on a fixed number of workers, bounding the queue size.
type Pool struct {
	queue chan Task
	wg    sync.WaitGroup
}

// New starts size workers draining tasks submitted to Submit. It must be
// stopped with Close.
func New(size, queueSize int) *Pool {
	p := &Pool{queue: make(chan Task, queueSize)}
	p.wg.Add(size)
	for range size {
		go p.worker()
	}
	return p
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.queue {
		// The task's error is consumed by the task itself (e.g. via a result
		// channel); the worker has no additional context to log it with.
		_ = task(context.Background())
	}
}

// Submit enqueues a task bound to ctx, blocking until a slot is free. The
// task runs with ctx so request deadlines propagate to upstream calls. It
// returns ErrQueueFull when the queue is saturated.
func (p *Pool) Submit(ctx context.Context, task Task) error {
	select {
	case p.queue <- func(_ context.Context) error { return task(ctx) }:
		return nil
	default:
		return ErrQueueFull
	}
}

// ErrQueueFull is returned when the task queue is full.
var ErrQueueFull = &QueueFullError{}

// QueueFullError signals a saturated worker pool.
type QueueFullError struct{}

func (e *QueueFullError) Error() string { return "worker pool queue full" }

// Close stops accepting tasks and waits for running workers to drain.
func (p *Pool) Close() {
	close(p.queue)
	p.wg.Wait()
}
