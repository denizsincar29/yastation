package queue

import (
	"context"
	"io"
	"log"
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
