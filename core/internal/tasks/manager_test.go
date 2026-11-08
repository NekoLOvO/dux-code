package tasks

import (
	"testing"
)

// Scheduling means unattended: when a run ends — or cannot even start — somebody must be told.
func TestHandleEventNotifiesOnFinish(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	st, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	task, err := st.Put(Task{
		Name:     "Daily summary",
		Prompt:   "summarize today's issues",
		Schedule: Schedule{Kind: ScheduleManual},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := NewManager(st, nil, nil, nil)
	m.running[task.ID] = "run-1"

	var got Task
	var status, detail string
	calls := 0
	m.OnFinish = func(x Task, s, d string) {
		calls++
		got = x
		status = s
		detail = d
	}

	m.HandleEvent(map[string]any{"kind": "run.finished", "runId": "run-1", "status": "ok"})

	if calls != 1 {
		t.Fatalf("OnFinish should be called once, got %d", calls)
	}
	if status != "ok" {
		t.Errorf("status = %q, want ok", status)
	}
	if detail != "" {
		t.Errorf("success must carry no error text: %q", detail)
	}
	if got.Name != "Daily summary" {
		t.Errorf("the task name was lost: %q", got.Name)
	}
	if got.LastStatus != "ok" {
		t.Errorf("LastStatus was not written back: %q", got.LastStatus)
	}
}

func TestHandleEventReportsFailureWithDetail(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	st, _ := Open()
	task, err := st.Put(Task{Name: "broken task", Prompt: "x", Schedule: Schedule{Kind: ScheduleManual}, Enabled: true})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := NewManager(st, nil, nil, nil)
	m.running[task.ID] = "run-2"

	var status, detail string
	m.OnFinish = func(x Task, s, d string) {
		status, detail = s, d
	}

	m.HandleEvent(map[string]any{"kind": "run.failed", "runId": "run-2", "error": "provider returned 400"})

	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
	if detail != "provider returned 400" {
		t.Errorf("detail = %q", detail)
	}
}

// Events the task did not produce must not notify.
func TestHandleEventIgnoresUnknownRun(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	st, _ := Open()
	if _, err := st.Put(Task{Name: "t", Prompt: "x", Schedule: Schedule{Kind: ScheduleManual}, Enabled: true}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := NewManager(st, nil, nil, nil)
	calls := 0
	m.OnFinish = func(Task, string, string) { calls++ }

	m.HandleEvent(map[string]any{"kind": "run.finished", "runId": "someone else's run", "status": "ok"})

	if calls != 0 {
		t.Errorf("a run that belongs to no task must not notify, but it fired %d times", calls)
	}
}

// "Previous run still going" is a skip, not a failure.
func TestTickBusyIsNotAFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	st, _ := Open()
	task, err := st.Put(Task{
		Name:     "overlap",
		Prompt:   "x",
		Schedule: Schedule{Kind: ScheduleEvery, Minutes: 1},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := NewManager(st, nil, nil, nil)
	calls := 0
	m.OnFinish = func(Task, string, string) { calls++ }
	m.running[task.ID] = "still running"

	m.tick()

	if calls != 0 {
		t.Errorf("an overlapping run must not notify, got %d", calls)
	}
}
