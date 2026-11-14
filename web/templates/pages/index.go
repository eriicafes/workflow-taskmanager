package pages

import "github.com/eriicafes/tmpl"

// Index is the dashboard once the bot is set up: its status, who may use
// it, and what each user keeps track of.
type Index struct {
	Setup      Setup
	Users      []User
	UsersError string
	Flash      Flash
}

func (p Index) Tmpl() tmpl.Template {
	return tmpl.Wrap(Layout{Title: "Task Manager"}, tmpl.Tmpl("pages/index", p))
}

// User is someone using the bot, with what they keep track of.
type User struct {
	Name     string
	Timezone string
	Paused   bool
	Tasks    []Task
	// Done is how many done tasks are kept.
	Done   int
	Habits []Habit
}

// Task is an open task, described for the page.
type Task struct {
	Title   string
	Due     string
	Overdue bool
}

// Habit is a habit, described for the page.
type Habit struct {
	Name     string
	Schedule string
	Streak   int
	Today    int
}

// Flash is a message shown once after a form posts.
type Flash struct {
	Message string `json:"message"`
	Error   bool   `json:"error,omitempty"`
}
