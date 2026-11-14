// Package agenda writes a user's tasks and habits as short plain-text
// lists for Telegram: the day's agenda, and the /tasks and /habits lists.
package agenda

import (
	"fmt"
	"strings"
	"time"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
)

// Clock is the layout of a time of day as the user reads it.
const Clock = "3:04 pm"

// When describes a local time relative to now, such as "today 5 pm",
// "tomorrow 9 am" or "Fri 14 Nov 9 am". With allDay, it leaves the time out.
func When(t, now time.Time, allDay bool) string {
	t = t.In(now.Location())
	day := dayName(t, now)
	if allDay {
		return day
	}
	return day + " " + t.Format(Clock)
}

func dayName(t, now time.Time) string {
	switch days := daysBetween(now, t); {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 1 && days < 7:
		return t.Format("Monday")
	case t.Year() == now.Year():
		return t.Format("Mon 2 Jan")
	default:
		return t.Format("Mon 2 Jan 2006")
	}
}

// daysBetween counts the calendar days from a to b in a's location.
func daysBetween(a, b time.Time) int {
	b = b.In(a.Location())
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

// Next returns when the agenda arrives next after after: at hour, local
// to loc. With hour -1, for no agenda, it is a day later, so the agenda's
// chain keeps going and picks up a new hour once the user sets one.
func Next(hour int, loc *time.Location, after time.Time) time.Time {
	if hour < 0 {
		return after.Add(24 * time.Hour)
	}
	local := after.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, loc)
	if !at.After(after) {
		at = time.Date(local.Year(), local.Month(), local.Day()+1, hour, 0, 0, 0, loc)
	}
	return at
}

// Overdue reports whether an open task is past due at now: past its time,
// or for an all-day task, past its day.
func Overdue(task store.Task, now time.Time) bool {
	if task.Done || task.Due.IsZero() {
		return false
	}
	if task.AllDay {
		return daysBetween(now, task.Due) < 0
	}
	return task.Due.Before(now)
}

// TaskLine describes an open task on one line.
func TaskLine(task store.Task, now time.Time) string {
	line := "• " + task.Title
	switch {
	case Overdue(task, now):
		line += " · overdue, was due " + When(task.Due, now, task.AllDay)
	case !task.Due.IsZero():
		line += " · due " + When(task.Due, now, task.AllDay)
	}
	if !task.RemindAt.IsZero() && task.RemindAt.After(now) {
		line += " · ⏰ " + When(task.RemindAt, now, false)
	}
	return line
}

// HabitLine describes a habit on one line, with its progress today.
func HabitLine(habit store.Habit, now time.Time) string {
	line := "• " + habit.Name + " · " + habit.Describe()
	if done := habit.DoneOn(now); done > 0 {
		if n := habit.RemindersOn(now.Weekday()); n > 1 {
			line += fmt.Sprintf(" · %d/%d today", done, n)
		} else {
			line += " · ✅ today"
		}
	}
	if streak := habit.CurrentStreak(now); streak > 1 {
		line += fmt.Sprintf(" · 🔥 %d", streak)
	}
	return line
}

// Today is the day's agenda for a user at now, their local time: tasks
// overdue and due today, today's habits, and how many tasks are coming up or
// undated. It is "" when there is nothing to say.
func Today(tasks []store.Task, habits []store.Habit, now time.Time) string {
	var due, later, undated []string
	for _, task := range tasks {
		switch {
		case task.Done:
		case task.Due.IsZero():
			undated = append(undated, TaskLine(task, now))
		case Overdue(task, now) || daysBetween(now, task.Due) == 0:
			due = append(due, TaskLine(task, now))
		case daysBetween(now, task.Due) < 7:
			later = append(later, TaskLine(task, now))
		}
	}
	var today []string
	for _, habit := range habits {
		if habit.On(now.Weekday()) {
			today = append(today, HabitLine(habit, now))
		}
	}
	if len(due)+len(today)+len(later)+len(undated) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📋 %s\n", now.Format("Monday, 2 January"))
	section(&b, "Due today", due)
	section(&b, "Habits", today)
	section(&b, "This week", later)
	section(&b, "Anytime", undated)
	return strings.TrimSpace(b.String())
}

// Tasks lists a user's open tasks at now, their local time.
func Tasks(tasks []store.Task, now time.Time) string {
	var overdue, due, later, undated []string
	for _, task := range tasks {
		switch {
		case task.Done:
		case Overdue(task, now):
			overdue = append(overdue, TaskLine(task, now))
		case task.Due.IsZero():
			undated = append(undated, TaskLine(task, now))
		case daysBetween(now, task.Due) == 0:
			due = append(due, TaskLine(task, now))
		default:
			later = append(later, TaskLine(task, now))
		}
	}
	if len(overdue)+len(due)+len(later)+len(undated) == 0 {
		return "No open tasks. Tell me what's coming up and I'll keep track of it 📝"
	}
	var b strings.Builder
	section(&b, "Overdue", overdue)
	section(&b, "Today", due)
	section(&b, "Coming up", later)
	section(&b, "Anytime", undated)
	return strings.TrimSpace(b.String())
}

// Habits lists a user's habits at now, their local time.
func Habits(habits []store.Habit, now time.Time) string {
	if len(habits) == 0 {
		return "No habits yet. Tell me one you'd like to build, like \"drink water every 2 hours\" or \"read at 9 pm\" 🌱"
	}
	lines := make([]string, len(habits))
	for i, habit := range habits {
		lines[i] = HabitLine(habit, now)
	}
	var b strings.Builder
	section(&b, "Habits", lines)
	return strings.TrimSpace(b.String())
}

// section writes a titled list, or nothing when lines is empty.
func section(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n%s\n", title, strings.Join(lines, "\n"))
}
