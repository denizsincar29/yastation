// Package queue makes slow, round-trip-heavy station calls (each one is a
// few HTTP requests to Yandex — creating/patching a scenario, then firing
// it, roughly 1-2s end to end) non-blocking for the caller: Enqueue
// returns immediately, and a single background worker drains the jobs in
// order, so two commands sent back-to-back still reach the speaker in the
// order they were issued instead of racing each other.
package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Job is one unit of work: a human-readable label (for logging/CLI
// feedback) and the function that actually talks to Yandex.
type Job struct {
	Label string
	Run   func() error
}

// Result is sent back on a Job's optional result channel, if the caller
// asked for one via EnqueueWait.
type Result struct {
	Label string
	Err   error
}

// Queue is a single-worker, ordered, non-blocking command queue.
type Queue struct {
	jobs   chan queuedJob
	done   chan struct{}
	wg     sync.WaitGroup
	logger *log.Logger
}

type queuedJob struct {
	job    Job
	result chan<- Result
}

// New starts the background worker. Buffer sets how many pending commands
// can queue up before Enqueue starts blocking the caller (a generous
// buffer, e.g. 100, keeps "instant return" true for normal interactive
// use; it only blocks if you fire commands faster than Yandex's API can
// possibly keep up with for a long time).
func New(buffer int, logger *log.Logger) *Queue {
	if logger == nil {
		logger = log.Default()
	}
	q := &Queue{
		jobs:   make(chan queuedJob, buffer),
		done:   make(chan struct{}),
		logger: logger,
	}
	q.wg.Add(1)
	go q.worker()
	return q
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case qj, ok := <-q.jobs:
			if !ok {
				return
			}
			err := qj.job.Run()
			if err != nil {
				q.logger.Printf("[очередь] ошибка выполняя %q: %v", qj.job.Label, err)
			}
			if qj.result != nil {
				qj.result <- Result{Label: qj.job.Label, Err: err}
				close(qj.result)
			}
		case <-q.done:
			return
		}
	}
}

// Enqueue schedules job and returns immediately unless the buffer is
// completely full, in which case it blocks briefly rather than silently
// dropping the command.
func (q *Queue) Enqueue(job Job) {
	q.jobs <- queuedJob{job: job}
}

// EnqueueWait schedules job and blocks the calling goroutine (not
// necessarily the whole program) until it has actually run, returning its
// error. Useful for the HTTP backend, where a request usually should wait
// for the actual outcome, but multiple requests still won't jam Yandex
// with concurrent scenario edits on the same speaker.
func (q *Queue) EnqueueWait(ctx context.Context, job Job) error {
	result := make(chan Result, 1)
	select {
	case q.jobs <- queuedJob{job: job, result: result}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case r := <-result:
		return r.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pending returns how many jobs are currently sitting in the buffer,
// waiting for the worker.
func (q *Queue) Pending() int {
	return len(q.jobs)
}

// Close stops accepting new jobs and waits for the worker to drain
// whatever is left, then returns.
func (q *Queue) Close() {
	close(q.jobs)
	q.wg.Wait()
}

// LabelF is a small convenience for building a Job's Label with fmt-style
// formatting without callers importing fmt everywhere.
func LabelF(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
