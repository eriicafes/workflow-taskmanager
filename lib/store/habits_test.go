package store

import (
	"testing"
	"time"
)

var lagos, _ = time.LoadLocation("Africa/Lagos")

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, lagos)
}

func TestScheduleNextDaily(t *testing.T) {
	s := Schedule{Times: []string{"20:00", "8:00", "08:00"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(s.Times) != 2 || s.Times[0] != "08:00" {
		t.Fatalf("Times = %v", s.Times)
	}
	// 13 Nov 2026 is a Friday.
	for _, c := range []struct{ after, want time.Time }{
		{at(13, 7, 0), at(13, 8, 0)},
		{at(13, 8, 0), at(13, 20, 0)},
		{at(13, 21, 0), at(14, 8, 0)},
	} {
		if got := s.Next(c.after, lagos); !got.Equal(c.want) {
			t.Errorf("Next(%v) = %v, want %v", c.after, got, c.want)
		}
	}
}

func TestScheduleNextHourlyOnWeekdays(t *testing.T) {
	s := Schedule{EveryHours: 3, FromHour: 9, ToHour: 18, Days: []string{"Friday", "mon"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := s.slots(); len(got) != 4 || got[3] != 18*60 {
		t.Fatalf("slots = %v", got)
	}
	if got := s.Next(at(13, 10, 0), lagos); !got.Equal(at(13, 12, 0)) {
		t.Errorf("same day = %v", got)
	}
	// After Friday's last, the next is Monday's first.
	if got := s.Next(at(13, 18, 0), lagos); !got.Equal(at(16, 9, 0)) {
		t.Errorf("next week day = %v", got)
	}
	if got := s.Describe(); got != "every 3 hours, 9:00 am to 6:00 pm, on Mon, Fri" {
		t.Errorf("Describe = %q", got)
	}
}

func TestScheduleValidate(t *testing.T) {
	for _, s := range []Schedule{
		{},
		{Times: []string{"25:00"}},
		{EveryHours: 13, FromHour: 9, ToHour: 21},
		{EveryHours: 2, FromHour: 22, ToHour: 6},
		{Times: []string{"08:00"}, EveryHours: 2},
		{Times: []string{"08:00"}, Days: []string{"someday"}},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v is valid", s)
		}
	}
	s := Schedule{Times: []string{"07:30"}, Days: []string{"sat", "sun"}}
	if err := s.Validate(); err != nil || s.Describe() != "at 7:30 am, on weekends" {
		t.Fatalf("Describe = %q, %v", s.Describe(), err)
	}
	daily := Schedule{Times: []string{"07:30"}}
	if daily.Describe() != "daily at 7:30 am" {
		t.Fatalf("Describe = %q", daily.Describe())
	}
}

func TestHabitStreaks(t *testing.T) {
	// Only on weekdays, so the weekend doesn't break the streak.
	h := Habit{Schedule: Schedule{Times: []string{"08:00"}, Days: []string{"mon", "tue", "wed", "thu", "fri"}}}
	h.LogDone(at(12, 9, 0))  // Thu
	h.LogDone(at(13, 9, 0))  // Fri
	h.LogDone(at(13, 19, 0)) // Fri again
	if h.Streak != 2 || h.DoneToday != 2 || h.Total != 3 {
		t.Fatalf("after Friday: %+v", h)
	}
	if got := h.CurrentStreak(at(16, 7, 0)); got != 2 {
		t.Fatalf("Monday morning streak = %d", got)
	}
	h.LogDone(at(16, 9, 0)) // Mon
	if h.Streak != 3 || h.Best != 3 || h.DoneOn(at(16, 20, 0)) != 1 {
		t.Fatalf("after Monday: %+v", h)
	}
	// Missing Tuesday breaks it.
	if got := h.CurrentStreak(at(18, 7, 0)); got != 0 {
		t.Fatalf("Wednesday streak = %d", got)
	}
	h.LogDone(at(18, 9, 0))
	if h.Streak != 1 || h.Best != 3 {
		t.Fatalf("after a gap: %+v", h)
	}
}
