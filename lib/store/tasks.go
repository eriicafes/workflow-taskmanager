package store

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// Task is something the user has to do, once.
type Task struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Notes string `json:"notes,omitempty"`
	// Due is when the task is due. With AllDay, only its local date counts.
	Due    time.Time `json:"due,omitzero"`
	AllDay bool      `json:"allDay,omitempty"`
	// RemindAt is when the user is reminded, or zero for no reminder. It is
	// cleared once the reminder is sent.
	RemindAt time.Time `json:"remindAt,omitzero"`
	// Version changes every time the reminder is scheduled, so a Reminder
	// run for an older schedule finds it changed and ends.
	Version   int       `json:"version,omitempty"`
	Done      bool      `json:"done,omitempty"`
	DoneAt    time.Time `json:"doneAt,omitzero"`
	CreatedAt time.Time `json:"createdAt"`
}

// Tasks returns the user's tasks: open ones first, by due date with undated
// last, then done ones, most recently done first.
func (s *Store) Tasks(ctx context.Context, chatID int64) ([]Task, error) {
	tasks, err := hashValues[Task](ctx, s, s.key("tasks", chatID))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(tasks, compareTasks)
	return tasks, nil
}

func compareTasks(a, b Task) int {
	if a.Done != b.Done {
		if a.Done {
			return 1
		}
		return -1
	}
	if a.Done {
		return b.DoneAt.Compare(a.DoneAt)
	}
	if a.Due.IsZero() != b.Due.IsZero() {
		if a.Due.IsZero() {
			return 1
		}
		return -1
	}
	return cmp.Or(a.Due.Compare(b.Due), a.CreatedAt.Compare(b.CreatedAt))
}

// Task returns one of the user's tasks.
func (s *Store) Task(ctx context.Context, chatID int64, id string) (Task, error) {
	return hashValue[Task](ctx, s, s.key("tasks", chatID), id, "task")
}

// NewTaskID returns an ID for a new task.
func (s *Store) NewTaskID(ctx context.Context, chatID int64) (string, error) {
	return s.nextID(ctx, chatID, "t")
}

// SaveTask creates or replaces a task.
func (s *Store) SaveTask(ctx context.Context, chatID int64, task Task) error {
	return s.hashSet(ctx, s.key("tasks", chatID), task.ID, task)
}

// DeleteTask removes a task.
func (s *Store) DeleteTask(ctx context.Context, chatID int64, id string) error {
	return s.rdb.HDel(ctx, s.key("tasks", chatID), id).Err()
}

// MaxDoneTasks is how many done tasks are kept. PruneTasks removes the
// oldest beyond it.
const MaxDoneTasks = 30

// PruneTasks removes the oldest done tasks beyond MaxDoneTasks.
func (s *Store) PruneTasks(ctx context.Context, chatID int64) error {
	tasks, err := s.Tasks(ctx, chatID)
	if err != nil {
		return err
	}
	var done []string
	for _, task := range tasks {
		if task.Done {
			done = append(done, task.ID)
		}
	}
	if len(done) <= MaxDoneTasks {
		return nil
	}
	return s.rdb.HDel(ctx, s.key("tasks", chatID), done[MaxDoneTasks:]...).Err()
}
