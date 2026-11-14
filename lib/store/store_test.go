package store

import (
	"errors"
	"testing"
	"time"

	"github.com/eriicafes/go-workflow/workflowai"
)

func TestBotAndAccess(t *testing.T) {
	s := NewTest(t)
	ctx := t.Context()
	if _, err := s.Bot(ctx); !errors.Is(err, ErrNoBot) {
		t.Fatalf("Bot before saving = %v", err)
	}
	if err := s.SaveBot(ctx, Bot{Token: "1:abc", Username: "tasks_bot"}); err != nil {
		t.Fatal(err)
	}
	bot, err := s.Bot(ctx)
	if err != nil || bot.Username != "tasks_bot" || len(bot.WebhookSecret) < 32 {
		t.Fatalf("Bot = %+v, %v", bot, err)
	}

	for i, id := range []int64{7, 8, 7} {
		if err := s.RequestAccess(ctx, AccessRequest{ID: id, FirstName: "Ada", At: time.Unix(int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := s.AccessRequests(ctx)
	if err != nil || len(requests) != 2 || requests[0].ID != 7 {
		t.Fatalf("AccessRequests = %+v, %v", requests, err)
	}
	if err := s.Allow(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Allowed(ctx, 7); !ok {
		t.Fatal("7 is not allowed")
	}
	if requests, _ := s.AccessRequests(ctx); len(requests) != 1 || requests[0].ID != 8 {
		t.Fatalf("Allow kept the request: %+v", requests)
	}
	if err := s.Disallow(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := s.AllowedUsers(ctx); len(allowed) != 0 {
		t.Fatalf("AllowedUsers = %v", allowed)
	}
}

func TestRequestAccessIsBounded(t *testing.T) {
	s := NewTest(t)
	for i := range maxRequests + 5 {
		if err := s.RequestAccess(t.Context(), AccessRequest{ID: int64(i + 1), At: time.Unix(int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	requests, _ := s.AccessRequests(t.Context())
	if len(requests) != maxRequests || requests[0].ID != maxRequests+5 {
		t.Fatalf("kept %d requests, newest %d", len(requests), requests[0].ID)
	}
}

func TestTasksOrderAndPrune(t *testing.T) {
	s := NewTest(t)
	ctx := t.Context()
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	add := func(title string, due time.Time, done bool) {
		id, err := s.NewTaskID(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		task := Task{ID: id, Title: title, Due: due, Done: done, CreatedAt: base}
		if done {
			task.DoneAt = base.Add(time.Duration(len(title)) * time.Minute)
		}
		if err := s.SaveTask(ctx, 1, task); err != nil {
			t.Fatal(err)
		}
	}
	add("undated", time.Time{}, false)
	add("later", base.Add(48*time.Hour), false)
	add("soon", base.Add(time.Hour), false)
	add("done", time.Time{}, true)
	tasks, err := s.Tasks(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, task := range tasks {
		titles = append(titles, task.Title)
	}
	if want := []string{"soon", "later", "undated", "done"}; len(titles) != 4 || titles[0] != want[0] || titles[1] != want[1] || titles[2] != want[2] || titles[3] != want[3] {
		t.Fatalf("order = %v", titles)
	}
	if tasks[0].ID != "t3" {
		t.Fatalf("ID = %q", tasks[0].ID)
	}
	for range MaxDoneTasks + 3 {
		add("done again", time.Time{}, true)
	}
	if err := s.PruneTasks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	tasks, _ = s.Tasks(ctx, 1)
	if len(tasks) != 3+MaxDoneTasks {
		t.Fatalf("after prune %d tasks", len(tasks))
	}
}

func TestContinueAgendaRun(t *testing.T) {
	s := NewTest(t)
	ctx := t.Context()
	if err := s.SetAgendaRun(ctx, 1, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ContinueAgendaRun(ctx, 1, "a", "b"); err != nil || !ok {
		t.Fatalf("continue a to b = %v, %v", ok, err)
	}
	// A replaced chain doesn't take the agenda back.
	if ok, _ := s.ContinueAgendaRun(ctx, 1, "a", "c"); ok {
		t.Fatal("stale chain continued")
	}
	if id, _ := s.AgendaRun(ctx, 1); id != "b" {
		t.Fatalf("AgendaRun = %q", id)
	}
}

func TestHistoryAndNotes(t *testing.T) {
	s := NewTest(t)
	ctx := t.Context()
	assistant := workflowai.ModelMessage{Role: workflowai.RoleAssistant, Content: []workflowai.ContentPart{{Type: workflowai.PartText, Text: "hi"}}}
	if err := s.AppendHistory(ctx, 1, []workflowai.ModelMessage{assistant}); err != nil {
		t.Fatal(err)
	}
	if err := s.Note(ctx, 1, "Sent a reminder."); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(ctx, 1)
	if err != nil || len(history) != 1 || history[0].Content[0].Text != NotePrefix+"Sent a reminder." {
		t.Fatalf("History = %+v, %v", history, err)
	}
}
