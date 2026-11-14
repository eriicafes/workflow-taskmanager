package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Habit is something the user wants to do regularly, with reminders either
// at set times of the day or every few hours within a window.
type Habit struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Schedule
	// Version changes every time the habit's reminders are rescheduled, so
	// a Reminder run for an older schedule finds it changed and ends.
	Version int `json:"version,omitempty"`
	// NextAt is when the next reminder is due.
	NextAt time.Time `json:"nextAt,omitzero"`
	// LastDone is the local date, YYYY-MM-DD, the habit was last done, and
	// DoneToday how many times it was done that day.
	LastDone  string `json:"lastDone,omitempty"`
	DoneToday int    `json:"doneToday,omitempty"`
	// Streak counts scheduled days in a row it was done, up to LastDone.
	Streak    int       `json:"streak,omitempty"`
	Best      int       `json:"best,omitempty"`
	Total     int       `json:"total,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Schedule is when a habit's reminders arrive, in the user's time zone.
// It is daily at Times, or, with EveryHours, every few hours from FromHour
// through ToHour. Days limits it to some weekdays.
type Schedule struct {
	// Times are times of day, HH:MM.
	Times      []string `json:"times,omitempty"`
	EveryHours int      `json:"everyHours,omitempty"`
	FromHour   int      `json:"fromHour,omitempty"`
	ToHour     int      `json:"toHour,omitempty"`
	// Days are lowercase weekday abbreviations, such as "mon"; empty is
	// every day.
	Days []string `json:"days,omitempty"`
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Validate checks the schedule and normalizes its times and days.
func (s *Schedule) Validate() error {
	if s.EveryHours > 0 {
		if len(s.Times) > 0 {
			return errors.New("give either times or everyHours, not both")
		}
		if s.EveryHours > 12 {
			return errors.New("everyHours is at most 12; use times for less frequent reminders")
		}
		if s.FromHour < 0 || s.ToHour > 23 || s.FromHour > s.ToHour {
			return errors.New("fromHour and toHour are hours 0 to 23, with fromHour not after toHour")
		}
	} else {
		if len(s.Times) == 0 {
			return errors.New("give times of day, or everyHours")
		}
		if len(s.Times) > 12 {
			return errors.New("at most 12 times a day")
		}
		for i, t := range s.Times {
			parsed, err := time.Parse("15:04", strings.TrimSpace(t))
			if err != nil {
				return fmt.Errorf("time %q is not HH:MM", t)
			}
			s.Times[i] = parsed.Format("15:04")
		}
		slices.Sort(s.Times)
		s.Times = slices.Compact(s.Times)
	}
	for i, day := range s.Days {
		day = strings.ToLower(strings.TrimSpace(day))
		if len(day) > 3 {
			day = day[:3]
		}
		if !slices.Contains(weekdays, day) {
			return fmt.Errorf("day %q is not a weekday such as mon", s.Days[i])
		}
		s.Days[i] = day
	}
	slices.SortFunc(s.Days, func(a, b string) int { return slices.Index(weekdays, a) - slices.Index(weekdays, b) })
	s.Days = slices.Compact(s.Days)
	if len(s.Days) == 7 {
		s.Days = nil
	}
	return nil
}

// slots returns the minutes after midnight the reminders arrive on a
// scheduled day, in order.
func (s Schedule) slots() []int {
	var slots []int
	if s.EveryHours > 0 {
		for h := s.FromHour; h <= s.ToHour; h += s.EveryHours {
			slots = append(slots, h*60)
		}
		return slots
	}
	for _, t := range s.Times {
		if parsed, err := time.Parse("15:04", t); err == nil {
			slots = append(slots, parsed.Hour()*60+parsed.Minute())
		}
	}
	slices.Sort(slots)
	return slots
}

// On reports whether the schedule runs on weekday.
func (s Schedule) On(weekday time.Weekday) bool {
	return len(s.Days) == 0 || slices.Contains(s.Days, weekdays[weekday])
}

// Next returns the first reminder strictly after after, in loc, or the zero
// time when the schedule has none.
func (s Schedule) Next(after time.Time, loc *time.Location) time.Time {
	local := after.In(loc)
	slots := s.slots()
	for d := range 8 {
		day := time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, loc)
		if !s.On(day.Weekday()) {
			continue
		}
		for _, minute := range slots {
			at := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, loc)
			if at.After(after) {
				return at
			}
		}
	}
	return time.Time{}
}

// RemindersOn returns how many reminders the schedule sends on a day.
func (s Schedule) RemindersOn(weekday time.Weekday) int {
	if !s.On(weekday) {
		return 0
	}
	return len(s.slots())
}

// Describe says when the schedule's reminders arrive, such as "daily at
// 8 am" or "every 2 hours, 9 am to 9 pm, on weekdays".
func (s Schedule) Describe() string {
	var b strings.Builder
	if s.EveryHours > 0 {
		if s.EveryHours == 1 {
			b.WriteString("hourly")
		} else {
			fmt.Fprintf(&b, "every %d hours", s.EveryHours)
		}
		fmt.Fprintf(&b, ", %s to %s", clock(s.FromHour*60), clock(s.ToHour*60))
	} else {
		times := make([]string, 0, len(s.Times))
		for _, minute := range s.slots() {
			times = append(times, clock(minute))
		}
		b.WriteString("at " + strings.Join(times, ", "))
	}
	switch strings.Join(s.Days, ",") {
	case "":
		if s.EveryHours == 0 {
			return "daily " + b.String()
		}
	case "mon,tue,wed,thu,fri":
		b.WriteString(", on weekdays")
	case "sun,sat":
		b.WriteString(", on weekends")
	default:
		names := make([]string, len(s.Days))
		for i, day := range s.Days {
			names[i] = strings.ToUpper(day[:1]) + day[1:]
		}
		b.WriteString(", on " + strings.Join(names, ", "))
	}
	return b.String()
}

func clock(minute int) string {
	return time.Date(2000, 1, 1, minute/60, minute%60, 0, 0, time.UTC).Format("3:04 pm")
}

// date formats a local day as YYYY-MM-DD.
func date(t time.Time) string { return t.Format("2006-01-02") }

// previousDay returns the scheduled day before the local day of t.
func (h Habit) previousDay(t time.Time) string {
	for d := 1; d <= 7; d++ {
		day := t.AddDate(0, 0, -d)
		if h.On(day.Weekday()) {
			return date(day)
		}
	}
	return ""
}

// LogDone records that the habit was done at now, the user's local time,
// keeping its streak: the streak goes on when the last time was today or
// on the previous scheduled day.
func (h *Habit) LogDone(now time.Time) {
	today := date(now)
	switch h.LastDone {
	case today:
		h.DoneToday++
	case h.previousDay(now):
		h.Streak++
		h.DoneToday = 1
	default:
		h.Streak = 1
		h.DoneToday = 1
	}
	h.LastDone = today
	h.Total++
	h.Best = max(h.Best, h.Streak)
}

// CurrentStreak returns the streak as of now, the user's local time: zero
// once a scheduled day passed without the habit.
func (h Habit) CurrentStreak(now time.Time) int {
	if h.LastDone == date(now) || h.LastDone == h.previousDay(now) {
		return h.Streak
	}
	return 0
}

// DoneOn returns how many times the habit was done on the local day of now.
func (h Habit) DoneOn(now time.Time) int {
	if h.LastDone == date(now) {
		return h.DoneToday
	}
	return 0
}

// Habits returns the user's habits by name.
func (s *Store) Habits(ctx context.Context, chatID int64) ([]Habit, error) {
	habits, err := hashValues[Habit](ctx, s, s.key("habits", chatID))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(habits, func(a, b Habit) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return habits, nil
}

// Habit returns one of the user's habits.
func (s *Store) Habit(ctx context.Context, chatID int64, id string) (Habit, error) {
	return hashValue[Habit](ctx, s, s.key("habits", chatID), id, "habit")
}

// HabitCount returns how many habits the user has.
func (s *Store) HabitCount(ctx context.Context, chatID int64) (int64, error) {
	return s.rdb.HLen(ctx, s.key("habits", chatID)).Result()
}

// NewHabitID returns an ID for a new habit.
func (s *Store) NewHabitID(ctx context.Context, chatID int64) (string, error) {
	return s.nextID(ctx, chatID, "h")
}

// SaveHabit creates or replaces a habit.
func (s *Store) SaveHabit(ctx context.Context, chatID int64, habit Habit) error {
	return s.hashSet(ctx, s.key("habits", chatID), habit.ID, habit)
}

// LogHabit records that a habit was done at now, the user's local time,
// and returns it.
func (s *Store) LogHabit(ctx context.Context, chatID int64, id string, now time.Time) (Habit, error) {
	habit, err := s.Habit(ctx, chatID, id)
	if err != nil {
		return Habit{}, err
	}
	habit.LogDone(now)
	return habit, s.SaveHabit(ctx, chatID, habit)
}

// DeleteHabit removes a habit.
func (s *Store) DeleteHabit(ctx context.Context, chatID int64, id string) error {
	return s.rdb.HDel(ctx, s.key("habits", chatID), id).Err()
}
