// Package scheduler implements the two trigger kinds the old Python
// prototype exposed as /every and /at: a repeating interval ("every 30s",
// "every 5m", "every 2h") and a one-shot daily time-of-day ("at 7:30").
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Run is called by the scheduler when a task fires. Implementations
// typically forward commandLine into the same dispatcher used for
// interactive input.
type Run func(commandLine string)

// Task is one scheduled job.
type Task struct {
	ID          int
	Spec        string // human-readable, e.g. "every 5m" or "at 07:30"
	CommandLine string
	cancel      func()
}

// Scheduler owns a set of running tasks. Safe for concurrent use.
type Scheduler struct {
	mu     sync.Mutex
	tasks  map[int]*Task
	nextID int
	run    Run
	now    func() time.Time // overridable for tests
}

// New creates a scheduler that calls run(commandLine) whenever a task
// fires.
func New(run Run) *Scheduler {
	return &Scheduler{tasks: map[int]*Task{}, run: run, now: time.Now}
}

// ParseInterval parses "30s", "5m", "2h" into a duration.
func ParseInterval(spec string) (time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, fmt.Errorf("пустой интервал")
	}
	unit := spec[len(spec)-1]
	numPart := spec[:len(spec)-1]
	n, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("не понял интервал %q, ожидал например 30s, 5m, 2h", spec)
	}
	switch unit {
	case 's':
		return time.Duration(n * float64(time.Second)), nil
	case 'm':
		return time.Duration(n * float64(time.Minute)), nil
	case 'h':
		return time.Duration(n * float64(time.Hour)), nil
	default:
		return 0, fmt.Errorf("неизвестная единица времени %q в %q (нужно s, m или h)", string(unit), spec)
	}
}

// nextAt returns the next occurrence (today if still ahead, else
// tomorrow) of HH:MM relative to now.
func nextAt(hhmm string, now time.Time) (time.Time, error) {
	parts := strings.SplitN(hhmm, ":", 2)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("не понял время %q, ожидал ЧЧ:ММ", hhmm)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("не понял время %q, ожидал ЧЧ:ММ", hhmm)
	}
	candidate := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate, nil
}

// ScheduleEvery starts a repeating task and returns it.
func (s *Scheduler) ScheduleEvery(interval time.Duration, spec, commandLine string) *Task {
	stop := make(chan struct{})
	t := s.newTask(spec, commandLine, func() { close(stop) })

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.run(commandLine)
			case <-stop:
				return
			}
		}
	}()
	return t
}

// ScheduleAt starts a one-shot task firing at the next occurrence of
// hh:mm, and returns it.
func (s *Scheduler) ScheduleAt(at time.Time, spec, commandLine string) *Task {
	stop := make(chan struct{})
	t := s.newTask(spec, commandLine, func() { close(stop) })

	go func() {
		timer := time.NewTimer(time.Until(at))
		defer timer.Stop()
		select {
		case <-timer.C:
			s.run(commandLine)
			s.remove(t.ID)
		case <-stop:
			return
		}
	}()
	return t
}

// Schedule parses a "every <interval>" or "at <HH:MM>" spec and starts
// the corresponding task.
func (s *Scheduler) Schedule(spec, commandLine string) (*Task, error) {
	fields := strings.Fields(spec)
	if len(fields) != 2 {
		return nil, fmt.Errorf(`ожидал "every <30s|5m|2h>" или "at <ЧЧ:ММ>", получил %q`, spec)
	}
	kind, value := fields[0], fields[1]
	switch kind {
	case "every":
		d, err := ParseInterval(value)
		if err != nil {
			return nil, err
		}
		return s.ScheduleEvery(d, spec, commandLine), nil
	case "at":
		at, err := nextAt(value, s.now())
		if err != nil {
			return nil, err
		}
		return s.ScheduleAt(at, spec, commandLine), nil
	default:
		return nil, fmt.Errorf(`неизвестный тип расписания %q, нужно "every" или "at"`, kind)
	}
}

func (s *Scheduler) newTask(spec, commandLine string, cancel func()) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	t := &Task{ID: s.nextID, Spec: spec, CommandLine: commandLine, cancel: cancel}
	s.tasks[t.ID] = t
	return t
}

func (s *Scheduler) remove(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
}

// List returns currently scheduled tasks, oldest first.
func (s *Scheduler) List() []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	// stable-ish order by ID
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// CancelAll stops every scheduled task.
func (s *Scheduler) CancelAll() {
	s.mu.Lock()
	tasks := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	s.tasks = map[int]*Task{}
	s.mu.Unlock()

	for _, t := range tasks {
		t.cancel()
	}
}
