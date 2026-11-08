package tasks

import (
	"testing"
	"time"
)

func at(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.Local)
}

func TestScheduleNextAfterDaily(t *testing.T) {
	s := Schedule{Kind: ScheduleDaily, At: "09:00"}

	// Not yet → today.
	if got, want := s.NextAfter(at(2026, 3, 10, 8, 0)), at(2026, 3, 10, 9, 0); !got.Equal(want) {
		t.Errorf("before 9 today: got %v, want %v", got, want)
	}
	// Already past → tomorrow.
	if got, want := s.NextAfter(at(2026, 3, 10, 10, 0)), at(2026, 3, 11, 9, 0); !got.Equal(want) {
		t.Errorf("after 9 today: got %v, want %v", got, want)
	}
	// Exactly on the hour is not "after", so it moves on to tomorrow.
	if got, want := s.NextAfter(at(2026, 3, 10, 9, 0)), at(2026, 3, 11, 9, 0); !got.Equal(want) {
		t.Errorf("exactly on the hour: got %v, want %v", got, want)
	}
}

func TestScheduleNextAfterWeekly(t *testing.T) {
	// 2026-03-10 is a Tuesday.
	if at(2026, 3, 10, 0, 0).Weekday() != time.Tuesday {
		t.Fatal("the test premise is wrong: 2026-03-10 should be a Tuesday")
	}
	s := Schedule{Kind: ScheduleWeekly, At: "09:00", Weekday: int(time.Thursday)}

	if got, want := s.NextAfter(at(2026, 3, 10, 8, 0)), at(2026, 3, 12, 9, 0); !got.Equal(want) {
		t.Errorf("the first Thursday after Tuesday: got %v, want %v", got, want)
	}
	// Same day but the time has passed → same weekday next week.
	if got, want := s.NextAfter(at(2026, 3, 12, 10, 0)), at(2026, 3, 19, 9, 0); !got.Equal(want) {
		t.Errorf("Thursday after 9: got %v, want %v", got, want)
	}
}

func TestScheduleNextAfterEvery(t *testing.T) {
	s := Schedule{Kind: ScheduleEvery, Minutes: 90}
	if got, want := s.NextAfter(at(2026, 3, 10, 8, 0)), at(2026, 3, 10, 9, 30); !got.Equal(want) {
		t.Errorf("every 90m: got %v, want %v", got, want)
	}
}

func TestScheduleManualHasNoNext(t *testing.T) {
	s := Schedule{Kind: ScheduleManual}
	if got := s.NextAfter(at(2026, 3, 10, 8, 0)); !got.IsZero() {
		t.Errorf("manual must have no next run, got %v", got)
	}
}

func TestBadScheduleReturnsZero(t *testing.T) {
	for _, s := range []Schedule{
		{Kind: ScheduleDaily, At: "9"},
		{Kind: ScheduleDaily, At: "25:00"},
		{Kind: ScheduleDaily, At: ""},
		{Kind: ScheduleEvery, Minutes: 0},
		{Kind: ScheduleWeekly, At: "09:00", Weekday: 9},
		{Kind: "hourly", At: "09:00"},
	} {
		if got := s.NextAfter(at(2026, 3, 10, 8, 0)); !got.IsZero() {
			t.Errorf("%+v should return the zero value, got %v", s, got)
		}
	}
}

// The heart of scheduling: missed runs are caught up, exactly once.
func TestMissedRunCatchesUpOnce(t *testing.T) {
	task := Task{
		ID: "t1", Name: "Daily summary", Prompt: "summarize yesterday's commits",
		Schedule:  Schedule{Kind: ScheduleDaily, At: "09:00"},
		Enabled:   true,
		CreatedAt: at(2026, 3, 8, 9, 0).UnixMilli(),
		LastRunAt: at(2026, 3, 8, 9, 0).UnixMilli(), // ran two days ago
	}

	now := at(2026, 3, 10, 10, 0) // 10:00 today, 09:00 long gone
	if !task.Due(now) {
		t.Fatal("after missing two runs it should catch up")
	}
	// The next time is 09:00 yesterday: one catch-up, not two.
	if got, want := task.NextRun(now), at(2026, 3, 9, 9, 0); !got.Equal(want) {
		t.Errorf("NextRun = %v, want %v", got, want)
	}

	// After the catch-up the baseline moves to now, so the next is 09:00 tomorrow rather than an
	// immediate second fire.
	task.LastRunAt = now.UnixMilli()
	if task.Due(now) {
		t.Error("right after running it must not still be due")
	}
	if got, want := task.NextRun(now), at(2026, 3, 11, 9, 0); !got.Equal(want) {
		t.Errorf("NextRun after the run = %v, want %v", got, want)
	}
}

func TestNotDueBeforeNextRun(t *testing.T) {
	task := Task{
		Schedule:  Schedule{Kind: ScheduleDaily, At: "09:00"},
		CreatedAt: at(2026, 3, 10, 8, 0).UnixMilli(),
	}
	if task.Due(at(2026, 3, 10, 8, 59)) {
		t.Error("before 9 it must not be due")
	}
	if !task.Due(at(2026, 3, 10, 9, 1)) {
		t.Error("after 9 it should be due")
	}
}

func TestValidate(t *testing.T) {
	ok := Task{Name: "n", Prompt: "p", Schedule: Schedule{Kind: ScheduleDaily, At: "09:00"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("a valid task was rejected: %v", err)
	}

	bad := []Task{
		{Name: "", Prompt: "p", Schedule: Schedule{Kind: ScheduleManual}},
		{Name: "n", Prompt: "  ", Schedule: Schedule{Kind: ScheduleManual}},
		{Name: "n", Prompt: "p", Schedule: Schedule{Kind: ScheduleEvery, Minutes: 0}},
		{Name: "n", Prompt: "p", Schedule: Schedule{Kind: ScheduleDaily, At: "9am"}},
		{Name: "n", Prompt: "p", Schedule: Schedule{Kind: "weekly", At: "09:00", Weekday: 7}},
	}
	for i, task := range bad {
		if err := task.Validate(); err == nil {
			t.Errorf("case %d should be rejected: %+v", i, task)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("the first Open should be empty")
	}

	created, err := s.Put(Task{
		Name: "Daily summary", Prompt: "summarize yesterday's commits",
		Schedule: Schedule{Kind: ScheduleDaily, At: "09:00"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if created.ID == "" || created.CreatedAt == 0 {
		t.Error("adding a task should fill in id and createdAt")
	}

	// Reopen to confirm it really reached the file.
	s2, err := Open()
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got, ok := s2.Get(created.ID)
	if !ok {
		t.Fatal("the task is gone after reloading")
	}
	if got.Name != "Daily summary" || got.Schedule.At != "09:00" {
		t.Errorf("fields were not stored correctly: %+v", got)
	}

	// Updating must not overwrite the run state the scheduler wrote.
	if _, err := s2.Patch(created.ID, func(t *Task) {
		t.LastRunAt = 12345
		t.LastStatus = "ok"
	}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	edited := got
	edited.Name = "renamed"
	if _, err := s2.Put(edited); err != nil {
		t.Fatalf("Put update: %v", err)
	}
	after, _ := s2.Get(created.ID)
	if after.Name != "renamed" {
		t.Error("the rename had no effect")
	}
	if after.LastRunAt != 12345 || after.LastStatus != "ok" {
		t.Errorf("saving the form must not overwrite the run state: %+v", after)
	}

	if err := s2.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s2.Get(created.ID); ok {
		t.Error("it is still found after deletion")
	}
}
