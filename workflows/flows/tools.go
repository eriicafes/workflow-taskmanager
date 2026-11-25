package flows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eriicafes/go-workflow"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// userTools holds the agent's tools for one user. They reach Flows'
// dependencies, the user's chat ID and time zone through the receiver, so the
// model's tool input is all they take, and the model can't pick another user.
//
// Tools that schedule reminders run in workflow context, so they can start
// Reminder runs, and reach the store through steps. The others run as steps.
type userTools struct {
	*Flows
	ChatID int64
	Loc    *time.Location
}

// Limits keep the work reminders schedule small.
const (
	maxOpenTasks     = 200
	maxHabits        = 20
	maxReminderAhead = 366 * 24 * time.Hour
)

// parseLocal reads a local date and time, YYYY-MM-DDTHH:MM, or with allDay
// a date, YYYY-MM-DD, in loc.
func parseLocal(value string, loc *time.Location) (t time.Time, allDay bool, err error) {
	value = strings.Replace(strings.TrimSpace(value), " ", "T", 1)
	if t, err := time.ParseInLocation(localLayout, value, loc); err == nil {
		return t, false, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", value, loc); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("%q is not YYYY-MM-DDTHH:MM or YYYY-MM-DD", value)
}

// parseReminder reads a reminder time, which must be in the future and at
// most maxReminderAhead away.
func parseReminder(value string, loc *time.Location, now time.Time) (time.Time, error) {
	at, allDay, err := parseLocal(value, loc)
	if err != nil {
		return time.Time{}, err
	}
	if allDay {
		return time.Time{}, errors.New("a reminder needs a time, YYYY-MM-DDTHH:MM")
	}
	if !at.After(now) {
		return time.Time{}, fmt.Errorf("reminder %s is in the past; it is now %s", value, now.In(loc).Format(localLayout))
	}
	if at.Sub(now) > maxReminderAhead {
		return time.Time{}, errors.New("reminders can be at most a year ahead")
	}
	return at, nil
}

type taskInput struct {
	Title    string `json:"title" desc:"What to do, short."`
	Notes    string `json:"notes,omitempty" desc:"Details worth keeping."`
	Due      string `json:"due,omitempty" desc:"Local due date and time, YYYY-MM-DDTHH:MM, or a date, YYYY-MM-DD, for a whole day."`
	RemindAt string `json:"remindAt,omitempty" desc:"Local time to remind the user, YYYY-MM-DDTHH:MM."`
}

// addTask saves a new task and starts its reminder's run.
func (u userTools) addTask(ctx context.Context, in taskInput) (taskView, error) {
	now := workflow.Now(ctx)
	var err error
	task := store.Task{Title: strings.TrimSpace(in.Title), Notes: strings.TrimSpace(in.Notes), CreatedAt: now}
	if task.Title == "" {
		return taskView{}, errors.New("title is required")
	}
	if in.Due != "" {
		if task.Due, task.AllDay, err = parseLocal(in.Due, u.Loc); err != nil {
			return taskView{}, err
		}
	}
	if in.RemindAt != "" {
		if task.RemindAt, err = parseReminder(in.RemindAt, u.Loc, now); err != nil {
			return taskView{}, err
		}
		task.Version = 1
	}
	task, err = workflow.Step(ctx, u.createTask, task)
	if err != nil {
		return taskView{}, err
	}
	if err := u.scheduleTask(ctx, task); err != nil {
		return taskView{}, err
	}
	return viewTask(task, u.Loc, now.In(u.Loc)), nil
}

// createTask gives a task its ID and saves it.
func (u userTools) createTask(ctx context.Context, task store.Task) (store.Task, error) {
	tasks, err := u.Store.Tasks(ctx, u.ChatID)
	if err != nil {
		return store.Task{}, err
	}
	open := 0
	for _, t := range tasks {
		if !t.Done {
			open++
		}
	}
	if open >= maxOpenTasks {
		return store.Task{}, workflow.FatalError(fmt.Errorf("the user already has %d open tasks, the most allowed; ask them to finish or delete some", open))
	}
	if task.ID, err = u.Store.NewTaskID(ctx, u.ChatID); err != nil {
		return store.Task{}, err
	}
	return task, u.Store.SaveTask(ctx, u.ChatID, task)
}

// scheduleTask starts the run that sends the task's reminder, if it has one.
func (u userTools) scheduleTask(ctx context.Context, task store.Task) error {
	if task.RemindAt.IsZero() || task.Done {
		return nil
	}
	_, err := workflow.Start(ctx, workflows.Reminder{ChatID: u.ChatID, Kind: workflows.KindTask, ItemID: task.ID, At: task.RemindAt, Version: task.Version}, workflow.StartOptions{Deployment: workflow.LatestDeployment})
	return err
}

type taskUpdate struct {
	ID       string `json:"id" desc:"The task's id."`
	Title    string `json:"title,omitempty" desc:"A new title."`
	Notes    string `json:"notes,omitempty" desc:"New notes, replacing the old ones."`
	Due      string `json:"due,omitempty" desc:"A new local due date and time, YYYY-MM-DDTHH:MM, or date, YYYY-MM-DD; \"none\" removes it."`
	RemindAt string `json:"remindAt,omitempty" desc:"A new local reminder time, YYYY-MM-DDTHH:MM; \"none\" removes it."`
	Done     *bool  `json:"done,omitempty" desc:"True when the user finished it, false to reopen it."`
}

// taskEdit is a taskUpdate with its times read, for the step that saves it.
type taskEdit struct {
	ID       string     `json:"id"`
	Title    string     `json:"title,omitempty"`
	Notes    string     `json:"notes,omitempty"`
	Due      *time.Time `json:"due,omitempty"`
	AllDay   bool       `json:"allDay,omitempty"`
	RemindAt *time.Time `json:"remindAt,omitempty"`
	Done     *bool      `json:"done,omitempty"`
	Now      time.Time  `json:"now"`
}

// updateTask changes a task, and reschedules its reminder when that changed.
func (u userTools) updateTask(ctx context.Context, in taskUpdate) (taskView, error) {
	now := workflow.Now(ctx)
	edit := taskEdit{ID: in.ID, Title: strings.TrimSpace(in.Title), Notes: strings.TrimSpace(in.Notes), Done: in.Done, Now: now}
	switch in.Due {
	case "":
	case "none":
		edit.Due = &time.Time{}
	default:
		due, allDay, err := parseLocal(in.Due, u.Loc)
		if err != nil {
			return taskView{}, err
		}
		edit.Due, edit.AllDay = &due, allDay
	}
	switch in.RemindAt {
	case "":
	case "none":
		edit.RemindAt = &time.Time{}
	default:
		at, err := parseReminder(in.RemindAt, u.Loc, now)
		if err != nil {
			return taskView{}, err
		}
		edit.RemindAt = &at
	}
	changed, err := workflow.Step(ctx, u.editTask, edit)
	if err != nil {
		return taskView{}, err
	}
	if changed.Reschedule {
		if err := u.scheduleTask(ctx, changed.Task); err != nil {
			return taskView{}, err
		}
	}
	return viewTask(changed.Task, u.Loc, now.In(u.Loc)), nil
}

type editedTask struct {
	Task store.Task `json:"task"`
	// Reschedule reports whether the task needs a new reminder run.
	Reschedule bool `json:"reschedule"`
}

func (u userTools) editTask(ctx context.Context, edit taskEdit) (editedTask, error) {
	task, err := u.Store.Task(ctx, u.ChatID, edit.ID)
	if errors.Is(err, store.ErrNotFound) {
		return editedTask{}, workflow.FatalError(err)
	} else if err != nil {
		return editedTask{}, err
	}
	reschedule := false
	if edit.Title != "" {
		task.Title = edit.Title
	}
	if edit.Notes != "" {
		task.Notes = edit.Notes
	}
	if edit.Due != nil {
		task.Due, task.AllDay = *edit.Due, edit.AllDay
	}
	if edit.RemindAt != nil {
		task.RemindAt = *edit.RemindAt
		task.Version++
		reschedule = true
	}
	if edit.Done != nil && *edit.Done != task.Done {
		task.Done = *edit.Done
		task.DoneAt = time.Time{}
		if task.Done {
			task.DoneAt = edit.Now
		} else if task.RemindAt.After(edit.Now) {
			// Reopening brings its pending reminder back.
			task.Version++
			reschedule = true
		}
	}
	if err := u.Store.SaveTask(ctx, u.ChatID, task); err != nil {
		return editedTask{}, err
	}
	if task.Done {
		if err := u.Store.PruneTasks(ctx, u.ChatID); err != nil {
			return editedTask{}, err
		}
	}
	return editedTask{Task: task, Reschedule: reschedule && !task.Done && !task.RemindAt.IsZero()}, nil
}

type idInput struct {
	ID string `json:"id" desc:"The id."`
}

// deleteTask deletes a task. Its reminder's run finds it gone and ends.
func (u userTools) deleteTask(ctx context.Context, in idInput) (string, error) {
	if _, err := u.Store.Task(ctx, u.ChatID, in.ID); errors.Is(err, store.ErrNotFound) {
		return "", workflow.FatalError(err)
	} else if err != nil {
		return "", err
	}
	return "Deleted.", u.Store.DeleteTask(ctx, u.ChatID, in.ID)
}

type habitInput struct {
	Name       string   `json:"name" desc:"The habit, short, like \"Drink water\"."`
	Times      []string `json:"times,omitempty" desc:"Local times of day to remind, HH:MM. Use these or everyHours."`
	EveryHours int      `json:"everyHours,omitempty" desc:"Remind every this many hours, 1 to 12, from fromHour through toHour."`
	FromHour   int      `json:"fromHour,omitempty" desc:"With everyHours, the local hour (0-23) of the first reminder each day."`
	ToHour     int      `json:"toHour,omitempty" desc:"With everyHours, the last local hour (0-23) a reminder may arrive."`
	Days       []string `json:"days,omitempty" desc:"Weekdays it runs on, like [\"mon\",\"wed\",\"fri\"]. Omit for every day."`
}

func (in habitInput) schedule() (store.Schedule, error) {
	s := store.Schedule{Times: in.Times, EveryHours: in.EveryHours, FromHour: in.FromHour, ToHour: in.ToHour, Days: in.Days}
	if s.EveryHours > 0 && s.FromHour == 0 && s.ToHour == 0 {
		s.FromHour, s.ToHour = 9, 21
	}
	return s, s.Validate()
}

// addHabit saves a new habit and starts the run for its first reminder.
func (u userTools) addHabit(ctx context.Context, in habitInput) (habitView, error) {
	now := workflow.Now(ctx)
	var err error
	habit := store.Habit{Name: strings.TrimSpace(in.Name), CreatedAt: now, Version: 1}
	if habit.Name == "" {
		return habitView{}, errors.New("name is required")
	}
	if habit.Schedule, err = in.schedule(); err != nil {
		return habitView{}, err
	}
	habit.NextAt = habit.Next(now, u.Loc)
	habit, err = workflow.Step(ctx, u.createHabit, habit)
	if err != nil {
		return habitView{}, err
	}
	if err := u.scheduleHabit(ctx, habit); err != nil {
		return habitView{}, err
	}
	return viewHabit(habit, u.Loc, now.In(u.Loc)), nil
}

func (u userTools) createHabit(ctx context.Context, habit store.Habit) (store.Habit, error) {
	count, err := u.Store.HabitCount(ctx, u.ChatID)
	if err != nil {
		return store.Habit{}, err
	}
	if count >= maxHabits {
		return store.Habit{}, workflow.FatalError(fmt.Errorf("the user already has %d habits, the most allowed; ask which one to drop", count))
	}
	if habit.ID, err = u.Store.NewHabitID(ctx, u.ChatID); err != nil {
		return store.Habit{}, err
	}
	return habit, u.Store.SaveHabit(ctx, u.ChatID, habit)
}

// scheduleHabit starts the run that sends the habit's next reminder.
func (u userTools) scheduleHabit(ctx context.Context, habit store.Habit) error {
	if habit.NextAt.IsZero() {
		return nil
	}
	_, err := workflow.Start(ctx, workflows.Reminder{ChatID: u.ChatID, Kind: workflows.KindHabit, ItemID: habit.ID, At: habit.NextAt, Version: habit.Version}, workflow.StartOptions{Deployment: workflow.LatestDeployment})
	return err
}

// habitUpdate lists habitInput's schedule fields itself, since the tool
// schema skips embedded fields.
type habitUpdate struct {
	ID         string   `json:"id" desc:"The habit's id."`
	Name       string   `json:"name,omitempty" desc:"A new name."`
	Times      []string `json:"times,omitempty" desc:"New local times of day to remind, HH:MM. Use these or everyHours."`
	EveryHours int      `json:"everyHours,omitempty" desc:"Remind every this many hours, 1 to 12, from fromHour through toHour."`
	FromHour   int      `json:"fromHour,omitempty" desc:"With everyHours, the local hour (0-23) of the first reminder each day."`
	ToHour     int      `json:"toHour,omitempty" desc:"With everyHours, the last local hour (0-23) a reminder may arrive."`
	Days       []string `json:"days,omitempty" desc:"Weekdays it runs on, like [\"mon\",\"wed\",\"fri\"]. Omit for every day."`
}

// updateHabit renames a habit or replaces its schedule, rescheduling its
// reminders when the schedule changed.
func (u userTools) updateHabit(ctx context.Context, in habitUpdate) (habitView, error) {
	now := workflow.Now(ctx)
	edit := habitEdit{ID: in.ID, Name: strings.TrimSpace(in.Name), Now: now}
	if len(in.Times) > 0 || in.EveryHours > 0 || len(in.Days) > 0 {
		schedule, err := habitInput{Times: in.Times, EveryHours: in.EveryHours, FromHour: in.FromHour, ToHour: in.ToHour, Days: in.Days}.schedule()
		if err != nil {
			return habitView{}, err
		}
		edit.Schedule = &schedule
	}
	habit, err := workflow.Step(ctx, u.editHabit, edit)
	if err != nil {
		return habitView{}, err
	}
	if edit.Schedule != nil {
		if err := u.scheduleHabit(ctx, habit); err != nil {
			return habitView{}, err
		}
	}
	return viewHabit(habit, u.Loc, now.In(u.Loc)), nil
}

type habitEdit struct {
	ID       string          `json:"id"`
	Name     string          `json:"name,omitempty"`
	Schedule *store.Schedule `json:"schedule,omitempty"`
	Now      time.Time       `json:"now"`
}

func (u userTools) editHabit(ctx context.Context, edit habitEdit) (store.Habit, error) {
	habit, err := u.Store.Habit(ctx, u.ChatID, edit.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Habit{}, workflow.FatalError(err)
	} else if err != nil {
		return store.Habit{}, err
	}
	if edit.Name != "" {
		habit.Name = edit.Name
	}
	if edit.Schedule != nil {
		habit.Schedule = *edit.Schedule
		habit.Version++
		habit.NextAt = habit.Next(edit.Now, u.Loc)
	}
	return habit, u.Store.SaveHabit(ctx, u.ChatID, habit)
}

type habitLog struct {
	Name      string `json:"name"`
	DoneToday int    `json:"doneToday"`
	Streak    int    `json:"streakDays"`
	Best      int    `json:"bestStreakDays"`
}

// logHabit records that the user did a habit just now.
func (u userTools) logHabit(ctx context.Context, in idInput) (habitLog, error) {
	habit, err := u.Store.LogHabit(ctx, u.ChatID, in.ID, time.Now().In(u.Loc))
	if errors.Is(err, store.ErrNotFound) {
		return habitLog{}, workflow.FatalError(err)
	} else if err != nil {
		return habitLog{}, err
	}
	return habitLog{Name: habit.Name, DoneToday: habit.DoneToday, Streak: habit.Streak, Best: habit.Best}, nil
}

// deleteHabit deletes a habit. Its reminder's run finds it gone and ends.
func (u userTools) deleteHabit(ctx context.Context, in idInput) (string, error) {
	if _, err := u.Store.Habit(ctx, u.ChatID, in.ID); errors.Is(err, store.ErrNotFound) {
		return "", workflow.FatalError(err)
	} else if err != nil {
		return "", err
	}
	return "Deleted.", u.Store.DeleteHabit(ctx, u.ChatID, in.ID)
}

type settingsInput struct {
	Name       string `json:"name,omitempty" desc:"What the user likes to be called."`
	Timezone   string `json:"timezone,omitempty" desc:"IANA time zone, for example Europe/London or Africa/Lagos."`
	AgendaHour *int   `json:"agendaHour,omitempty" desc:"Local hour (0-23) the day's agenda arrives, or -1 to turn it off."`
	Paused     *bool  `json:"paused,omitempty" desc:"True to pause habit reminders and the agenda, false to resume them. Task reminders still fire."`
}

type settingsChange struct {
	User store.User `json:"user"`
	// Habits are the habits rescheduled for a new time zone.
	Habits []store.Habit `json:"habits,omitempty"`
	// AgendaMoved reports whether the agenda's time changed.
	AgendaMoved bool `json:"agendaMoved,omitempty"`
}

// updateSettings changes the user's settings. A new time zone moves the
// habits' reminders to the same local times there, and a new agenda time
// starts a new agenda chain, which the old one yields to.
func (u userTools) updateSettings(ctx context.Context, in settingsInput) (store.User, error) {
	if in.Timezone != "" {
		if _, err := time.LoadLocation(in.Timezone); err != nil {
			return store.User{}, fmt.Errorf("unknown time zone %q, use an IANA name such as Europe/London", in.Timezone)
		}
	}
	if in.AgendaHour != nil && (*in.AgendaHour < -1 || *in.AgendaHour > 23) {
		return store.User{}, errors.New("agendaHour is 0 to 23, or -1 for none")
	}
	change, err := workflow.Step(ctx, u.saveSettings, in)
	if err != nil {
		return store.User{}, err
	}
	for _, habit := range change.Habits {
		if err := u.scheduleHabit(ctx, habit); err != nil {
			return store.User{}, err
		}
	}
	if change.AgendaMoved {
		// The new chain replaces the old one, which ends when it sees it is
		// no longer the user's current chain.
		now := workflow.Now(ctx)
		run, err := workflow.Start(ctx, workflows.Agenda{ChatID: u.ChatID, At: agenda.Next(change.User.AgendaHour, change.User.Location(), now)}, workflow.StartOptions{Deployment: workflow.LatestDeployment})
		if err != nil {
			return store.User{}, err
		}
		if _, err := workflow.Step(ctx, u.saveAgendaRun, scoped[string]{ChatID: u.ChatID, Input: run.ID()}); err != nil {
			return store.User{}, err
		}
	}
	return change.User, nil
}

func (u userTools) saveSettings(ctx context.Context, in settingsInput) (settingsChange, error) {
	user, err := u.Store.User(ctx, u.ChatID)
	if err != nil {
		return settingsChange{}, err
	}
	var change settingsChange
	if in.Name != "" {
		user.Name = in.Name
	}
	if in.Paused != nil {
		user.Paused = *in.Paused
	}
	if in.AgendaHour != nil && *in.AgendaHour != user.AgendaHour {
		user.AgendaHour = *in.AgendaHour
		change.AgendaMoved = true
	}
	if in.Timezone != "" && in.Timezone != user.Timezone {
		user.Timezone = in.Timezone
		change.AgendaMoved = true
		habits, err := u.Store.Habits(ctx, u.ChatID)
		if err != nil {
			return settingsChange{}, err
		}
		for _, habit := range habits {
			habit.Version++
			habit.NextAt = habit.Next(time.Now(), user.Location())
			if err := u.Store.SaveHabit(ctx, u.ChatID, habit); err != nil {
				return settingsChange{}, err
			}
			change.Habits = append(change.Habits, habit)
		}
	}
	change.User = user
	return change, u.Store.SaveUser(ctx, user)
}
