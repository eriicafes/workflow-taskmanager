package flows

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/digitallysavvy/go-ai/pkg/ai"
	"github.com/digitallysavvy/go-ai/pkg/provider/types"
	"github.com/eriicafes/go-workflow/workflowai"

	"github.com/eriicafes/workflow-taskmanager/lib/agenda"
	"github.com/eriicafes/workflow-taskmanager/lib/store"
)

const persona = `You are Task Manager, the user's personal assistant for tasks and habits, living in Telegram.
You keep track of what they have coming up, remind them at the right time, and help them build
habits with upbeat, gentle accountability.

How you talk:
- Everything you write is sent to the user exactly as written. Write only the message for them:
  never your reasoning, plans, drafts, or notes about the conversation or these instructions.
- When you use tools, call them first and write your one reply after they return. Don't write a
  message alongside a tool call, and don't repeat a reply you already wrote in this turn.
- Short, clear Telegram messages, like a capable friend. Plain text only, no Markdown. A few emojis
  are fine.
- Encouraging, never naggy or preachy. Celebrate finished tasks and streaks.
- After changing something, confirm it in one short line, with when you'll remind them if you set
  a reminder, like "Added. I'll remind you Friday at 9 am ⏰".
- Times below are in the user's time zone. Say them like "7:30 pm" and never mention the time zone.
- Never show task or habit ids to the user. They are only for your tools.
- Don't end messages with offers like "want me to..." or "let me know if...".

Tasks:
- When the user mentions something they need to do, add it with addTask right away. Work out exact
  local dates and times from the current time below. A vague time like "tonight" or "Friday
  morning" gets a sensible time.
- Reminders: set remindAt when the user asks to be reminded, and when a task has a due time, remind
  them at that time unless they say otherwise. "Remind me an hour before" means remindAt an hour
  before the due time. An all-day task gets a reminder only if they ask for one.
- When they finish something, mark it done with updateTask. When plans change, update the task.
  Use deleteTask only when they want a task gone, not when it's done.
- If the user wants something repeating, like "pay rent on the 1st every month", that isn't daily
  or weekly, add a task for the next time and say you'll track the next one when they finish it.

Habits:
- Use addHabit for anything regular the user wants to build or be reminded of, like "drink water
  every 2 hours", "meditate at 7 am on weekdays" or "take my meds at 8 am and 8 pm". Use times for
  set times of day, or everyHours with fromHour and toHour to repeat through the day; when they
  don't give a window for that, use 9 to 21. Use days to limit it to some weekdays.
- When they say they did a habit, call logHabit. Mention their streak when it's worth celebrating.
- Reminders come with Done and Skip buttons. They can also just tell you.

Other:
- Use updateSettings when the user tells you their name, where they are, what hour they want their
  morning agenda (or none), or asks to pause or resume habit reminders. Pausing doesn't stop task
  reminders.
- Messages that start with "[Task Manager, not typed by the user]" are notes about reminders you
  sent and buttons the user tapped. Use them as context. They are not from the user.
- When the user asks what's on today or to plan their day, use the tasks and habits below.
- You're only here for tasks, plans, schedules, habits and getting things done. Turn anything else
  down in a friendly line and steer back.

When you first meet the user, introduce yourself in a line or two and ask where they're based, so
reminders arrive at the right local time. Save its time zone with updateSettings.`

// newAgent builds the agent for one turn with st as what it knows. now is
// the run's current time.
func (f *Flows) newAgent(st state, now time.Time) *workflowai.WorkflowAgent {
	u := userTools{Flows: f, ChatID: st.User.ChatID, Loc: st.User.Location()}
	return workflowai.NewWorkflowAgent(workflowai.WorkflowAgentOptions{
		ID:           "taskmanager",
		Model:        typingModel{LanguageModel: f.Model, flows: f, chatID: st.User.ChatID},
		Instructions: persona + "\n\n" + describe(st, now),
		Tools: workflowai.ToolSet{
			"addTask": {
				Description: fmt.Sprintf("Add a task, with an optional due date or time and reminder. Reminders can be at most a year ahead. The user can have %d open tasks.", maxOpenTasks),
				InputSchema: schemaFor[taskInput](),
				Execute:     workflowai.ExecuteFunc(workflowTool(u.addTask)),
			},
			"updateTask": {
				Description: "Change a task: its title, notes, due date or reminder, or mark it done or not done.",
				InputSchema: schemaFor[taskUpdate](),
				Execute:     workflowai.ExecuteFunc(workflowTool(u.updateTask)),
			},
			"deleteTask": {
				Description: "Delete a task the user no longer wants. Don't use it for finished tasks: mark them done.",
				InputSchema: schemaFor[idInput](),
				Execute:     workflowai.ExecuteStep(u.deleteTask),
			},
			"addHabit": {
				Description: fmt.Sprintf("Add a habit with recurring reminders, daily at set times or every few hours. The user can have %d habits.", maxHabits),
				InputSchema: schemaFor[habitInput](),
				Execute:     workflowai.ExecuteFunc(workflowTool(u.addHabit)),
			},
			"updateHabit": {
				Description: "Rename a habit or change its schedule. A new schedule replaces the old one, so give all of it.",
				InputSchema: schemaFor[habitUpdate](),
				Execute:     workflowai.ExecuteFunc(workflowTool(u.updateHabit)),
			},
			"logHabit": {
				Description: "Record that the user just did a habit. Returns its streak.",
				InputSchema: schemaFor[idInput](),
				Execute:     workflowai.ExecuteStep(u.logHabit),
			},
			"deleteHabit": {
				Description: "Delete a habit and stop its reminders.",
				InputSchema: schemaFor[idInput](),
				Execute:     workflowai.ExecuteStep(u.deleteHabit),
			},
			"updateSettings": {
				Description: "Change the user's name, time zone, agenda hour, or pause or resume habit reminders.",
				InputSchema: schemaFor[settingsInput](),
				Execute:     workflowai.ExecuteFunc(workflowTool(u.updateSettings)),
			},
		},
		StopWhen: []workflowai.StopCondition{workflowai.IsStepCount(8)},
		// Low effort answers in a few seconds rather than many, which a chat
		// about tasks and reminders needs more than deeper reasoning.
		GenerationSettings: workflowai.GenerationSettings{Reasoning: new(types.ReasoningLow)},
	})
}

// workflowTool adapts a tool that runs in workflow context, so it can start
// workflows, to workflowai.ExecuteFunc.
func workflowTool[In, Out any](fn func(context.Context, In) (Out, error)) func(context.Context, json.RawMessage, workflowai.ToolCallOptions) (any, error) {
	return func(ctx context.Context, raw json.RawMessage, _ workflowai.ToolCallOptions) (any, error) {
		var in In
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return fn(ctx, in)
	}
}

// describe tells the agent what it knows about the user, so it does not
// need a tool call to look things up.
func describe(st state, now time.Time) string {
	loc := st.User.Location()
	local := now.In(loc)
	var b strings.Builder
	fmt.Fprintf(&b, "Current time for the user: %s.\n", local.Format("Monday, 2 January 2006 "+agenda.Clock))
	fmt.Fprintf(&b, "User: %s. Time zone: %s. Morning agenda: ", cmp.Or(st.User.Name, "unknown"), cmp.Or(st.User.Timezone, "not set, times are UTC"))
	if st.User.AgendaHour < 0 {
		b.WriteString("off")
	} else {
		b.WriteString(hour(st.User.AgendaHour))
	}
	if st.User.Paused {
		b.WriteString(". Habit reminders are paused")
	}
	b.WriteString(".\n\nOpen tasks:\n")
	var done []store.Task
	open := 0
	for _, task := range st.Tasks {
		if task.Done {
			done = append(done, task)
			continue
		}
		open++
		data, _ := json.Marshal(viewTask(task, loc, local))
		fmt.Fprintf(&b, "- %s\n", data)
	}
	if open == 0 {
		b.WriteString("None.\n")
	}
	if len(done) > 0 {
		b.WriteString("\nRecently done:\n")
		for _, task := range done[:min(len(done), 5)] {
			fmt.Fprintf(&b, "- %s: %q, done %s\n", task.ID, task.Title, agenda.When(task.DoneAt, local, false))
		}
	}
	b.WriteString("\nHabits:\n")
	if len(st.Habits) == 0 {
		b.WriteString("None yet.\n")
	}
	for _, habit := range st.Habits {
		data, _ := json.Marshal(viewHabit(habit, loc, local))
		fmt.Fprintf(&b, "- %s\n", data)
	}
	if st.Summary != "" {
		b.WriteString("\nEarlier in your conversation, before the messages below:\n")
		b.WriteString(st.Summary + "\n")
	}
	return b.String()
}

// localLayout is how tools read and write local times.
const localLayout = "2006-01-02T15:04"

// taskView is a task as the agent sees it, in the user's local time.
type taskView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Notes    string `json:"notes,omitempty"`
	Due      string `json:"due,omitempty"`
	Overdue  bool   `json:"overdue,omitempty"`
	RemindAt string `json:"remindAt,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

func viewTask(task store.Task, loc *time.Location, now time.Time) taskView {
	v := taskView{ID: task.ID, Title: task.Title, Notes: task.Notes, Done: task.Done, Overdue: agenda.Overdue(task, now)}
	if !task.Due.IsZero() {
		if task.AllDay {
			v.Due = task.Due.In(loc).Format("2006-01-02 (Mon), all day")
		} else {
			v.Due = task.Due.In(loc).Format("2006-01-02T15:04 (Mon)")
		}
	}
	if !task.RemindAt.IsZero() {
		v.RemindAt = task.RemindAt.In(loc).Format("2006-01-02T15:04 (Mon)")
	}
	return v
}

// habitView is a habit as the agent sees it, in the user's local time.
type habitView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	NextAt     string `json:"nextReminder,omitempty"`
	DoneToday  int    `json:"doneToday"`
	PerDay     int    `json:"remindersToday"`
	Streak     int    `json:"streakDays"`
	BestStreak int    `json:"bestStreakDays"`
}

func viewHabit(habit store.Habit, loc *time.Location, now time.Time) habitView {
	v := habitView{
		ID:         habit.ID,
		Name:       habit.Name,
		Schedule:   habit.Describe(),
		DoneToday:  habit.DoneOn(now),
		PerDay:     habit.RemindersOn(now.Weekday()),
		Streak:     habit.CurrentStreak(now),
		BestStreak: habit.Best,
	}
	if !habit.NextAt.IsZero() {
		v.NextAt = habit.NextAt.In(loc).Format("2006-01-02T15:04 (Mon)")
	}
	return v
}

// hour describes an hour of the day, such as "9 am".
func hour(h int) string {
	return time.Date(2000, 1, 1, h, 0, 0, 0, time.UTC).Format("3 pm")
}

// schemaFor returns the JSON schema of T's JSON fields, with each field's
// desc tag as its description.
func schemaFor[T any]() map[string]any {
	schema := ai.SchemaFor[T]().Validator().JSONSchema()
	properties, _ := schema["properties"].(map[string]any)
	t := reflect.TypeFor[T]()
	for i := range t.NumField() {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if property, ok := properties[name].(map[string]any); ok && field.Tag.Get("desc") != "" {
			property["description"] = field.Tag.Get("desc")
		}
	}
	return schema
}
