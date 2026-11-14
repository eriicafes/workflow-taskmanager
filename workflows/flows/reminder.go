package flows

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eriicafes/go-workflow"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// maxSleep bounds how long one Reminder run sleeps before handing off to the
// next. go-workflow sleeps longer on its own, but a run stays on the
// deployment it started on, and each handoff starts the next run on the
// latest deployment, so a reminder far ahead isn't lost when old deployments
// are deleted.
const maxSleep = 6 * 24 * time.Hour

// Reminder is one link of a reminder's chain of runs. It sleeps toward
// input.At for at most maxSleep; if the reminder isn't due yet it starts the
// next run toward the same time and ends. Once due, it sends the reminder,
// and for a habit starts the run for its next reminder. Each run lasts days
// at most, and Reminder embeds workflow.StartOnLatest, so chains move to new
// deployments instead of outliving their first one.
//
// A run whose task or habit is gone, done, or rescheduled under a newer
// Version ends without sending, so nothing needs cancelling.
func (f *Flows) Reminder(ctx context.Context, input workflows.Reminder) (workflows.ReminderResult, error) {
	var result workflows.ReminderResult
	now, err := workflow.Now(ctx)
	if err != nil {
		return result, err
	}
	if wait := input.At.Sub(now); wait > 0 {
		if err := workflow.Sleep(ctx, min(wait, maxSleep)); err != nil {
			return result, err
		}
		if now, err = workflow.Now(ctx); err != nil {
			return result, err
		}
	}
	if input.At.Sub(now) > 0 {
		current, err := workflow.Step(ctx, f.reminderCurrent, input)
		if err != nil || !current {
			return result, err
		}
		_, err = workflow.Start(ctx, input)
		return result, err
	}
	fired, err := workflow.Step(ctx, f.fireReminder, input)
	if err != nil {
		return result, err
	}
	result.Sent = fired.Sent
	if fired.Next.IsZero() {
		return result, nil
	}
	next := input
	next.At = fired.Next
	_, err = workflow.Start(ctx, next)
	return result, err
}

// reminderCurrent reports whether the reminder's task or habit still
// exists with the reminder's Version.
func (f *Flows) reminderCurrent(ctx context.Context, input workflows.Reminder) (bool, error) {
	var version int
	var err error
	switch input.Kind {
	case workflows.KindTask:
		var task store.Task
		task, err = f.Store.Task(ctx, input.ChatID, input.ItemID)
		version = task.Version
		if task.Done {
			return false, nil
		}
	case workflows.KindHabit:
		var habit store.Habit
		habit, err = f.Store.Habit(ctx, input.ChatID, input.ItemID)
		version = habit.Version
	default:
		return false, workflow.FatalError(fmt.Errorf("unknown reminder kind %q", input.Kind))
	}
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && version == input.Version, err
}

type firedReminder struct {
	Sent bool `json:"sent"`
	// Next is when a habit's next reminder is due, or zero for none.
	Next time.Time `json:"next,omitzero"`
}

// fireReminder sends a due reminder with buttons to act on it, notes it in
// the conversation, and for a habit works out its next reminder.
func (f *Flows) fireReminder(ctx context.Context, input workflows.Reminder) (firedReminder, error) {
	current, err := f.reminderCurrent(ctx, input)
	if err != nil || !current {
		return firedReminder{}, err
	}
	user, err := f.Store.User(ctx, input.ChatID)
	if err != nil {
		return firedReminder{}, err
	}
	now := time.Now().In(user.Location())
	if input.Kind == workflows.KindTask {
		return firedReminder{Sent: true}, f.remindTask(ctx, user, input.ItemID, now)
	}
	return f.remindHabit(ctx, user, input, now)
}

func (f *Flows) remindTask(ctx context.Context, user store.User, id string, now time.Time) error {
	task, err := f.Store.Task(ctx, user.ChatID, id)
	if err != nil {
		return err
	}
	text := "⏰ " + task.Title
	if !task.Due.IsZero() {
		text += "\nDue " + agenda.When(task.Due, now, task.AllDay)
	}
	if task.Notes != "" {
		text += "\n" + task.Notes
	}
	if err := f.send(ctx, user.ChatID, text, [][]telegram.InlineKeyboardButton{{
		{Text: "Done ✅", CallbackData: "td:" + task.ID},
		{Text: "In 1 hour ⏰", CallbackData: "tz:" + task.ID},
	}}); err != nil {
		return err
	}
	task.RemindAt = time.Time{}
	if err := f.Store.SaveTask(ctx, user.ChatID, task); err != nil {
		return err
	}
	return f.Store.Note(ctx, user.ChatID, fmt.Sprintf("Sent the user a reminder for task %s %q.", task.ID, task.Title))
}

func (f *Flows) remindHabit(ctx context.Context, user store.User, input workflows.Reminder, now time.Time) (firedReminder, error) {
	habit, err := f.Store.Habit(ctx, user.ChatID, input.ItemID)
	if err != nil {
		return firedReminder{}, err
	}
	// Reminders that were late, say after downtime, are not made up.
	habit.NextAt = habit.Next(later(input.At, now), user.Location())
	if err := f.Store.SaveHabit(ctx, user.ChatID, habit); err != nil {
		return firedReminder{}, err
	}
	fired := firedReminder{Next: habit.NextAt}
	// A habit reminded once a day needs no reminder once it's done.
	doneAlready := habit.RemindersOn(now.Weekday()) == 1 && habit.DoneOn(now) > 0
	if user.Paused || doneAlready {
		return fired, nil
	}
	text := "🔁 " + habit.Name
	if streak := habit.CurrentStreak(now); streak > 1 {
		text += fmt.Sprintf("\n🔥 %d day streak", streak)
	}
	if err := f.send(ctx, user.ChatID, text, [][]telegram.InlineKeyboardButton{{
		{Text: "Done ✅", CallbackData: "hd:" + habit.ID},
		{Text: "Skip", CallbackData: "hs:" + habit.ID},
	}}); err != nil {
		return firedReminder{}, err
	}
	fired.Sent = true
	return fired, f.Store.Note(ctx, user.ChatID, fmt.Sprintf("Sent the user a reminder for habit %s %q.", habit.ID, habit.Name))
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// send sends text with a keyboard.
func (f *Flows) send(ctx context.Context, chatID int64, text string, keyboard [][]telegram.InlineKeyboardButton) error {
	bot, err := f.bot(ctx)
	if err != nil {
		return err
	}
	_, err = bot.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: &telegram.InlineKeyboardMarkup{InlineKeyboard: keyboard},
	})
	return err
}
