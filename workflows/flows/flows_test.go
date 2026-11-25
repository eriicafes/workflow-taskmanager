package flows

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/worlds"
	localworld "github.com/eriicafes/go-workflow/worlds/local"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/store/storetest"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/workflows"
)

// sentMessages fakes Telegram and collects the messages the bot sends.
type sentMessages struct {
	mu   sync.Mutex
	text []string
	data []string
}

func (s *sentMessages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Text   string                         `json:"text"`
		Markup *telegram.InlineKeyboardMarkup `json:"reply_markup"`
	}
	_ = json.NewDecoder(r.Body).Decode(&params)
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		s.mu.Lock()
		s.text = append(s.text, params.Text)
		if params.Markup != nil {
			for _, button := range params.Markup.InlineKeyboard[0] {
				s.data = append(s.data, button.CallbackData)
			}
		}
		s.mu.Unlock()
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1, "chat": map[string]any{"id": 1}, "date": 0}})
}

func (s *sentMessages) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.text...)
}

// testFlows runs Flows' runners on a local World over an in-memory store,
// with a connected bot whose messages are collected. The model is unset, so
// only workflows that don't chat can run.
func testFlows(t *testing.T) (*Flows, context.Context, *sentMessages) {
	t.Helper()
	sent := &sentMessages{}
	server := httptest.NewServer(sent)
	t.Cleanup(server.Close)
	previous := telegram.APIURL
	telegram.APIURL = server.URL
	t.Cleanup(func() { telegram.APIURL = previous })

	f := &Flows{Store: storetest.New(t)}
	world, err := localworld.Open(localworld.Config{Dir: t.TempDir(), BaseURL: "http://localhost/v1/workflow"},
		worlds.Runners(workflow.Use(f.Reminder), workflow.Use(f.Agenda)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { world.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	if err := world.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.SaveBot(ctx, store.Bot{Token: "1:test"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Store.EnsureUser(ctx, 1, "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	return f, workflow.NewContext(ctx, workflow.NewClient(world)), sent
}

func wait[Out any](t *testing.T, ctx context.Context, run *workflow.Run[Out]) Out {
	t.Helper()
	out, err := run.Value(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTaskReminder(t *testing.T) {
	f, ctx, sent := testFlows(t)
	soon := time.Now().Add(500 * time.Millisecond)
	task := store.Task{ID: "t1", Title: "Pay rent", Notes: "Landlord's account", RemindAt: soon, Version: 2}
	if err := f.Store.SaveTask(ctx, 1, task); err != nil {
		t.Fatal(err)
	}
	// A run for an older schedule ends without sending.
	stale, err := workflow.Start(ctx, workflows.Reminder{ChatID: 1, Kind: workflows.KindTask, ItemID: "t1", At: soon, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if wait(t, ctx, stale).Sent {
		t.Fatal("stale reminder was sent")
	}
	run, err := workflow.Start(ctx, workflows.Reminder{ChatID: 1, Kind: workflows.KindTask, ItemID: "t1", At: soon, Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !wait(t, ctx, run).Sent {
		t.Fatal("reminder not sent")
	}
	if got := sent.all(); len(got) != 1 || got[0] != "⏰ Pay rent\nLandlord's account" {
		t.Fatalf("sent %q", got)
	}
	if sent.data[0] != "td:t1" || sent.data[1] != "tz:t1" {
		t.Fatalf("buttons %v", sent.data)
	}
	if task, _ := f.Store.Task(ctx, 1, "t1"); !task.RemindAt.IsZero() {
		t.Fatal("reminder time not cleared")
	}
	if history, _ := f.Store.History(ctx, 1); len(history) != 1 || !strings.Contains(history[0].Content[0].Text, "reminder for task t1") {
		t.Fatalf("history %+v", history)
	}
}

func TestHabitReminderChains(t *testing.T) {
	f, ctx, sent := testFlows(t)
	soon := time.Now().Add(500 * time.Millisecond)
	habit := store.Habit{ID: "h1", Name: "Drink water", Schedule: store.Schedule{EveryHours: 2, FromHour: 0, ToHour: 23}, Version: 1, NextAt: soon}
	if err := f.Store.SaveHabit(ctx, 1, habit); err != nil {
		t.Fatal(err)
	}
	run, err := workflow.Start(ctx, workflows.Reminder{ChatID: 1, Kind: workflows.KindHabit, ItemID: "h1", At: soon, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !wait(t, ctx, run).Sent {
		t.Fatal("habit reminder not sent")
	}
	if got := sent.all(); len(got) != 1 || got[0] != "🔁 Drink water" {
		t.Fatalf("sent %q", got)
	}
	saved, _ := f.Store.Habit(ctx, 1, "h1")
	if !saved.NextAt.After(time.Now()) || saved.NextAt.Sub(time.Now()) > 2*time.Hour {
		t.Fatalf("next reminder at %v", saved.NextAt)
	}
}

func TestAgendaSendsAndYields(t *testing.T) {
	f, ctx, sent := testFlows(t)
	user, _ := f.Store.User(ctx, 1)
	user.AgendaHour = time.Now().UTC().Hour()
	if err := f.Store.SaveUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.SaveTask(ctx, 1, store.Task{ID: "t1", Title: "Ship it", Due: time.Now()}); err != nil {
		t.Fatal(err)
	}
	run, err := workflow.Start(ctx, workflows.Agenda{ChatID: 1, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// Not the recorded chain: it ends without sending.
	if wait(t, ctx, run).Sent || len(sent.all()) != 0 {
		t.Fatal("an unrecorded chain sent the agenda")
	}

	// The app starts an agenda run ahead of its time and records it while
	// it sleeps, so it is the current chain once it wakes.
	run, err = workflow.Start(ctx, workflows.Agenda{ChatID: 1, At: time.Now().Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Store.SetAgendaRun(ctx, 1, run.ID()); err != nil {
		t.Fatal(err)
	}
	if !wait(t, ctx, run).Sent {
		t.Fatal("agenda not sent")
	}
	if got := sent.all(); len(got) != 1 || !strings.Contains(got[0], "Ship it") {
		t.Fatalf("sent %q", got)
	}
	next, _ := f.Store.AgendaRun(ctx, 1)
	if next == "" || next == run.ID() {
		t.Fatalf("chain didn't hand off: %q", next)
	}
}

func TestParseLocal(t *testing.T) {
	loc, _ := time.LoadLocation("Africa/Lagos")
	at, allDay, err := parseLocal("2026-11-14 09:30", loc)
	if err != nil || allDay || !at.Equal(time.Date(2026, 11, 14, 9, 30, 0, 0, loc)) {
		t.Fatalf("parseLocal = %v, %v, %v", at, allDay, err)
	}
	if _, allDay, err := parseLocal("2026-11-14", loc); err != nil || !allDay {
		t.Fatalf("date = %v, %v", allDay, err)
	}
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, loc)
	if _, err := parseReminder("2026-11-14T09:00", loc, now); err == nil {
		t.Fatal("a past reminder parsed")
	}
	if _, err := parseReminder("2026-11-15", loc, now); err == nil {
		t.Fatal("a reminder without a time parsed")
	}
}

func TestHabitInputDefaultsWindow(t *testing.T) {
	s, err := habitInput{EveryHours: 2}.schedule()
	if err != nil || s.FromHour != 9 || s.ToHour != 21 {
		t.Fatalf("schedule = %+v, %v", s, err)
	}
}

func TestSchemaHasDescriptions(t *testing.T) {
	schema := schemaFor[taskInput]()
	properties := schema["properties"].(map[string]any)
	if properties["due"].(map[string]any)["description"] == nil {
		t.Fatalf("due has no description: %v", schema)
	}
	update := schemaFor[habitUpdate]()
	if _, ok := update["properties"].(map[string]any)["everyHours"]; !ok {
		t.Fatalf("habitUpdate lacks the embedded schedule fields: %v", update)
	}
}

func TestUndash(t *testing.T) {
	for in, want := range map[string]string{
		"Nice one — marked both done":  "Nice one, marked both done",
		"Rent's sorted—nice":           "Rent's sorted, nice",
		"Water every 2 hours, 9–9":     "Water every 2 hours, 9-9",
		"Done – see you at 7:30 pm":    "Done, see you at 7:30 pm",
		"No dashes here, just a comma": "No dashes here, just a comma",
	} {
		if got := undash(in); got != want {
			t.Errorf("undash(%q) = %q, want %q", in, got, want)
		}
	}
}
