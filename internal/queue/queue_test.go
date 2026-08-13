package queue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

func silentLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestEnqueueReturnsImmediately(t *testing.T) {
	q := New(10, silentLogger())
	defer q.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	q.Enqueue(Job{Label: "slow", Run: func() error {
		close(started)
		<-release
		return nil
	}})

	<-started // make sure the worker actually picked it up

	done := make(chan struct{})
	go func() {
		q.Enqueue(Job{Label: "instant-return-check", Run: func() error { return nil }})
		close(done)
	}()

	select {
	case <-done:
		// good: Enqueue returned without waiting for "slow" to finish
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Enqueue blocked instead of returning immediately")
	}
	close(release)
}

func TestJobsRunInOrder(t *testing.T) {
	q := New(10, silentLogger())

	var mu sync.Mutex
	var order []int

	for i := 0; i < 20; i++ {
		i := i
		q.Enqueue(Job{Label: "job", Run: func() error {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			return nil
		}})
	}
	q.Close() // waits for the worker to drain everything

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 20 {
		t.Fatalf("expected 20 jobs to run, got %d", len(order))
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("jobs ran out of order: %v", order)
		}
	}
}

func TestEnqueueWaitReturnsActualError(t *testing.T) {
	q := New(10, silentLogger())
	defer q.Close()

	err := q.EnqueueWait(context.Background(), Job{Label: "boom", Run: func() error {
		return errBoom
	}})
	if err != errBoom {
		t.Fatalf("expected errBoom, got %v", err)
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

func TestErrorLoggedOnlyForFireAndForgetJobs(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	q := New(10, logger)
	defer q.Close()

	// EnqueueWait: caller gets the error back directly, so the queue
	// itself must NOT also log it — that's the double-print bug.
	err := q.EnqueueWait(context.Background(), Job{Label: "waited", Run: func() error {
		return errors.New("boom")
	}})
	if err == nil {
		t.Fatal("expected error from EnqueueWait")
	}
	if strings.Contains(buf.String(), "waited") {
		t.Fatalf("queue logged an error for a job the caller was already waiting on: %q", buf.String())
	}

	// Plain Enqueue (fire-and-forget): nobody else will ever see this
	// error, so the queue logging it is correct and necessary.
	done := make(chan struct{})
	q.Enqueue(Job{Label: "forgotten", Run: func() error {
		defer close(done)
		return errors.New("kaboom")
	}})
	<-done
	// give the logger a moment; Run's error is logged right after it returns
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(buf.String(), "forgotten") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(buf.String(), "forgotten") {
		t.Fatalf("expected the fire-and-forget job's error to be logged, got %q", buf.String())
	}
}
