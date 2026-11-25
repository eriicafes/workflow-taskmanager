// Package workflows declares Task Manager's workflow types. Handlers start
// runs of them without importing their runners in workflows/flows.
//
// A run that starts another starts it on the latest deployment, with
// workflow.LatestDeployment, so chains of runs move to new deployments as
// they hand off and none is stranded on a deleted one. Handlers start runs
// with no options: they already run on the latest deployment.
package workflows

import (
	"time"

	"github.com/eriicafes/go-workflow"
)

// Conversation answers a user's mailbox in order. One runs per user at a
// time, and it ends once the mailbox stays empty for a while. Use Deliver
// rather than starting it directly.
type Conversation struct {
	workflow.Struct[ConversationResult]
	ChatID int64 `json:"chatId"`
}

type ConversationResult struct {
	// Turns is how many times the agent answered.
	Turns int `json:"turns"`
	// Failed is how many of those turns failed after telling the user.
	Failed int `json:"failed,omitempty"`
}

// Reminder kinds.
const (
	KindTask  = "task"
	KindHabit = "habit"
)

// Reminder sends the reminder of a task or a habit at At. Each run waits at
// most a few days and then starts the next run toward the same time, and a
// habit's run starts the run for its next reminder once it fires. A run ends
// without sending when the item is gone or its Version changed, which is how
// rescheduling and deleting cancel reminders.
type Reminder struct {
	workflow.Struct[ReminderResult]
	ChatID int64 `json:"chatId"`
	// Kind is KindTask or KindHabit, and ItemID the task's or habit's ID.
	Kind    string    `json:"kind"`
	ItemID  string    `json:"itemId"`
	At      time.Time `json:"at"`
	Version int       `json:"version"`
}

type ReminderResult struct {
	Sent bool `json:"sent"`
}

// Agenda sends the user the day's tasks and habits at their agenda hour,
// then starts the run for the next day.
type Agenda struct {
	workflow.Struct[AgendaResult]
	ChatID int64     `json:"chatId"`
	At     time.Time `json:"at"`
}

type AgendaResult struct {
	Sent bool `json:"sent"`
}
