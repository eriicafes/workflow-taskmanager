package agenda

import (
	"strings"
	"testing"
	"time"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
)

var lagos, _ = time.LoadLocation("Africa/Lagos")

func TestNext(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 30, 0, 0, lagos)
	if got := Next(8, lagos, now); !got.Equal(time.Date(2026, 11, 14, 8, 0, 0, 0, lagos)) {
		t.Errorf("Next(8) = %v", got)
	}
	if got := Next(10, lagos, now); !got.Equal(time.Date(2026, 11, 13, 10, 0, 0, 0, lagos)) {
		t.Errorf("Next(10) = %v", got)
	}
	if got := Next(-1, lagos, now); !got.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("Next(-1) = %v", got)
	}
}

func TestToday(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, lagos) // Friday
	tasks := []store.Task{
		{Title: "Pay rent", Due: now.Add(9 * time.Hour)},
		{Title: "Call bank", Due: now.Add(-48 * time.Hour), AllDay: true},
		{Title: "Book flights", Due: now.Add(72 * time.Hour)},
		{Title: "Read a book"},
		{Title: "Old", Done: true},
	}
	habits := []store.Habit{{Name: "Water", Schedule: store.Schedule{EveryHours: 2, FromHour: 9, ToHour: 21}}}
	got := Today(tasks, habits, now)
	for _, want := range []string{"Friday, 13 November", "Pay rent · due today 5:00 pm", "Call bank · overdue, was due Wed 11 Nov", "Book flights · due Monday", "Water · every 2 hours", "Anytime\n• Read a book"} {
		if !strings.Contains(got, want) {
			t.Errorf("agenda lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Old") {
		t.Error("agenda lists a done task")
	}
	if Today(nil, nil, now) != "" {
		t.Error("empty agenda is not empty")
	}
}

func TestOverdueAllDay(t *testing.T) {
	now := time.Date(2026, 11, 13, 23, 0, 0, 0, lagos)
	today := store.Task{Due: time.Date(2026, 11, 13, 0, 0, 0, 0, lagos), AllDay: true}
	if Overdue(today, now) {
		t.Fatal("an all-day task is overdue on its day")
	}
	if !Overdue(today, now.Add(2*time.Hour)) {
		t.Fatal("an all-day task isn't overdue the next day")
	}
}
