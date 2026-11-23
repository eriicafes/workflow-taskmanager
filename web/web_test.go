package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/eriicafes/go-workflow"
	"github.com/eriicafes/go-workflow/worlds"
	localworld "github.com/eriicafes/go-workflow/worlds/local"
	"github.com/eriicafes/httpx/httperrors"
	"github.com/eriicafes/tmpl/vite"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/store/storetest"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/web/templates"
)

const testToken = "123456:AAH4kTestTokenTestTokenTestToken123"

// fakeTelegram answers Bot API calls for testToken and records them.
type fakeTelegram struct {
	mu      sync.Mutex
	webhook string
	secret  string
	sent    []string
	edited  []string
	answers []string
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	if token != testToken {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 123456, "is_bot": true, "first_name": "My Tasks", "username": "tasks_bot"}
	case "getWebhookInfo":
		result = map[string]any{"url": f.webhook, "pending_update_count": 0}
	case "setWebhook":
		f.webhook, f.secret = params["url"].(string), params["secret_token"].(string)
	case "deleteWebhook":
		f.webhook = ""
	case "sendMessage":
		f.sent = append(f.sent, params["text"].(string))
		result = map[string]any{"message_id": len(f.sent), "chat": map[string]any{"id": params["chat_id"], "type": "private"}, "date": 0}
	case "editMessageText":
		f.edited = append(f.edited, params["text"].(string))
	case "answerCallbackQuery":
		f.answers = append(f.answers, params["text"].(string))
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeTelegram) lastSent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

// testApp returns an App on an in-memory store, whose bot calls a fake
// Telegram, with a local World to start runs on.
func testApp(t *testing.T) (*App, *fakeTelegram) {
	t.Helper()
	t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "tasks.example.com")
	fake := &fakeTelegram{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	previous := telegram.APIURL
	telegram.APIURL = server.URL
	t.Cleanup(func() { telegram.APIURL = previous })

	// A production Vite over an in-memory build, so tests need no dev server.
	v, err := vite.New(vite.Config{Output: fstest.MapFS{
		".vite/manifest.json":    {Data: []byte(`{"web/src/main.css":{"file":"assets/main-abc123.css","src":"web/src/main.css","isEntry":true}}`)},
		"assets/main-abc123.css": {Data: []byte("body{}")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := templates.Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	world, err := localworld.Open(localworld.Config{Dir: t.TempDir(), BaseURL: "http://localhost/v1/workflow"}, worlds.Runners())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { world.Close() })
	if err := world.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	app := &App{Store: storetest.New(t), Client: workflow.NewClient(world), Vite: v, Templates: parsed, Password: "hunter2"}
	if app.Session, app.Flash, err = cookies(app.Password, false); err != nil {
		t.Fatal(err)
	}
	return app, fake
}

// browser keeps cookies across requests, like a browser on the same origin.
type browser struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]*http.Cookie
}

func newBrowser(t *testing.T, handler http.Handler) *browser {
	return &browser{t: t, handler: handler, cookies: map[string]*http.Cookie{}}
}

func (b *browser) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest(method, "https://tasks.example.com"+path, strings.NewReader(form.Encode()))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for _, cookie := range b.cookies {
		req.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	b.handler.ServeHTTP(res, req)
	for _, cookie := range res.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	return res
}

func (b *browser) page(path string, wants ...string) string {
	b.t.Helper()
	res := b.do("GET", path, nil)
	body := res.Body.String()
	if res.Code != http.StatusOK {
		b.t.Fatalf("GET %s: %d %s", path, res.Code, body)
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			b.t.Errorf("GET %s lacks %q", path, want)
		}
	}
	return body
}

func wantRedirect(t *testing.T, res *httptest.ResponseRecorder, to string) {
	t.Helper()
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != to {
		t.Fatalf("got %d to %q, want a redirect to %s", res.Code, res.Header().Get("Location"), to)
	}
}

// update posts a Telegram update to the webhook with secret.
func update(t *testing.T, app *App, secret string, body string) int {
	t.Helper()
	req := httptest.NewRequest("POST", TelegramWebhookPath, strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	return res.Code
}

var updateID int

func message(from int64, text string) string {
	updateID++
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":1,"from":{"id":%d,"is_bot":false,"first_name":"Ada","username":"ada"},"chat":{"id":%d,"type":"private"},"date":0,"text":%q}}`, updateID, from, from, text)
}

func callback(from int64, data string) string {
	updateID++
	return fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"q%d","from":{"id":%d,"is_bot":false,"first_name":"Ada"},"message":{"message_id":9,"chat":{"id":%d,"type":"private"},"date":0},"data":%q}}`, updateID, updateID, from, from, data)
}

func TestLogin(t *testing.T) {
	app, _ := testApp(t)
	b := newBrowser(t, app.Handler())
	wantRedirect(t, b.do("GET", "/", nil), "/login")
	res := b.do("POST", "/login", url.Values{"password": {"wrong"}})
	if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "isn&#39;t right") {
		t.Fatalf("wrong password: %d", res.Code)
	}
	wantRedirect(t, b.do("POST", "/login", url.Values{"password": {"hunter2"}}), "/")
	b.page("/", "Set up Task Manager", "/assets/main-abc123.css")
	wantRedirect(t, b.do("POST", "/logout", url.Values{}), "/login")
	wantRedirect(t, b.do("GET", "/", nil), "/login")
}

func TestLoginWithoutPassword(t *testing.T) {
	app, _ := testApp(t)
	app.Password = ""
	b := newBrowser(t, app.Handler())
	if res := b.do("GET", "/login", nil); !strings.Contains(res.Body.String(), "SETUP_PASSWORD") {
		t.Fatal("login page does not ask for SETUP_PASSWORD")
	}
}

// TestSetupGuide walks through the guide: create the bot, save its token,
// connect the webhook, and allow the owner after they message the bot.
func TestSetupGuide(t *testing.T) {
	app, fake := testApp(t)
	b := newBrowser(t, app.Handler())
	wantRedirect(t, b.do("POST", "/login", url.Values{"password": {"hunter2"}}), "/")

	b.page("/", "Create a bot with BotFather", "https://t.me/BotFather", "/newbot", "0 of 4 done", "Save your bot&#39;s token first.")

	// Step 2: a bad token, then a revoked one, then the real one.
	wantRedirect(t, b.do("POST", "/setup/token", url.Values{"token": {"not a token"}, "from": {"guide"}}), "/?guide=1")
	b.page("/?guide=1", "doesn&#39;t look like a bot token")
	wantRedirect(t, b.do("POST", "/setup/token", url.Values{"token": {"999:AAH4kRevokedRevokedRevokedRevoked1"}, "from": {"guide"}}), "/?guide=1")
	b.page("/?guide=1", "doesn&#39;t recognize this token")
	wantRedirect(t, b.do("POST", "/setup/token", url.Values{"token": {" " + testToken + " "}, "from": {"guide"}}), "/?guide=1")
	body := b.page("/?guide=1", "Found your bot, @tasks_bot", "2 of 4 done", "123456:••••••••", "Connect bot", "https://tasks.example.com")
	if strings.Contains(body, testToken) {
		t.Fatal("the page shows the token")
	}

	// Step 3: Telegram only delivers to https.
	wantRedirect(t, b.do("POST", "/setup/connect", url.Values{"public_url": {"http://tasks.example.com"}, "from": {"guide"}}), "/?guide=1")
	b.page("/?guide=1", "only delivers to https")
	wantRedirect(t, b.do("POST", "/setup/connect", url.Values{"public_url": {"https://tasks.example.com/"}, "from": {"guide"}}), "/?guide=1")
	saved, _ := app.Store.Bot(t.Context())
	if fake.webhook != "https://tasks.example.com/telegram" || fake.secret != saved.WebhookSecret || fake.secret == "" {
		t.Fatalf("webhook %q with secret %q, saved %q", fake.webhook, fake.secret, saved.WebhookSecret)
	}
	b.page("/", "3 of 4 done", "Waiting for your message", "https://t.me/tasks_bot?start=setup", "@userinfobot", "location.reload")

	// Step 4: the owner messages the bot, which tells them their ID, and
	// they show up on the page to allow.
	if code := update(t, app, "wrong", message(42, "/start")); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", code)
	}
	if code := update(t, app, saved.WebhookSecret, message(42, "/start")); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	if got := fake.lastSent(); !strings.Contains(got, "Your Telegram user ID is 42") || !strings.Contains(got, "Allow") {
		t.Fatalf("reply to a stranger = %q", got)
	}
	body = b.page("/", "Ada", "@ada", "ID <span class=\"font-mono\">42</span>", "/setup/users/allow")
	if strings.Contains(body, "location.reload") {
		t.Fatal("the guide still reloads once a request arrived")
	}
	wantRedirect(t, b.do("POST", "/setup/users/allow", url.Values{"id": {"42"}, "from": {"guide"}}), "/?guide=1")
	if got := fake.lastSent(); !strings.Contains(got, "You're in") {
		t.Fatalf("welcome = %q", got)
	}
	b.page("/?guide=1", "4 of 4 done", "You're all set", "Go to dashboard")

	// Done: / is the dashboard.
	b.page("/", "Access", "Tasks &amp; habits", "● Connected", "Setup guide")

	// Using a different bot sends the guide back to step 2.
	wantRedirect(t, b.do("POST", "/setup/reset", url.Values{}), "/")
	if fake.webhook != "" {
		t.Fatal("reset left the webhook")
	}
	b.page("/", "Paste your bot&#39;s token", "1 of 4 done")
}

// connected returns an app set up for user 42.
func connected(t *testing.T) (*App, *fakeTelegram, string) {
	t.Helper()
	app, fake := testApp(t)
	ctx := t.Context()
	if err := app.Store.SaveBot(ctx, store.Bot{Token: testToken, Username: "tasks_bot"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.Allow(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.Store.EnsureUser(ctx, 42, "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	bot, _ := app.Store.Bot(ctx)
	return app, fake, bot.WebhookSecret
}

func TestCommands(t *testing.T) {
	app, fake, secret := connected(t)
	ctx := t.Context()
	if err := app.Store.SaveTask(ctx, 42, store.Task{ID: "t1", Title: "Pay rent", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for command, want := range map[string]string{
		"/id":     "Your Telegram user ID is 42",
		"/tasks":  "Pay rent",
		"/habits": "No habits yet",
		"/today":  "Pay rent",
		"/pause":  "paused",
	} {
		if code := update(t, app, secret, message(42, command)); code != http.StatusOK {
			t.Fatalf("%s: %d", command, code)
		}
		if got := fake.lastSent(); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", command, got, want)
		}
	}
	if user, _ := app.Store.User(ctx, 42); !user.Paused {
		t.Fatal("/pause didn't pause")
	}
	if id, _ := app.Store.AgendaRun(ctx, 42); id == "" {
		t.Fatal("no agenda chain started")
	}
}

func TestReminderButtons(t *testing.T) {
	app, fake, secret := connected(t)
	ctx := t.Context()
	if err := app.Store.SaveTask(ctx, 42, store.Task{ID: "t1", Title: "Pay rent", Version: 1}); err != nil {
		t.Fatal(err)
	}
	habit := store.Habit{ID: "h1", Name: "Read", Schedule: store.Schedule{Times: []string{"21:00"}}}
	if err := app.Store.SaveHabit(ctx, 42, habit); err != nil {
		t.Fatal(err)
	}

	update(t, app, secret, callback(42, "tz:t1"))
	task, _ := app.Store.Task(ctx, 42, "t1")
	if task.Version != 2 || time.Until(task.RemindAt) < 55*time.Minute {
		t.Fatalf("snoozed task = %+v", task)
	}
	update(t, app, secret, callback(42, "td:t1"))
	if task, _ := app.Store.Task(ctx, 42, "t1"); !task.Done {
		t.Fatal("task not done")
	}
	update(t, app, secret, callback(42, "hd:h1"))
	if habit, _ := app.Store.Habit(ctx, 42, "h1"); habit.Total != 1 || habit.Streak != 1 {
		t.Fatalf("habit = %+v", habit)
	}
	update(t, app, secret, callback(42, "hs:h1"))
	update(t, app, secret, callback(42, "td:t9"))
	update(t, app, secret, callback(7, "td:t1"))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	wantEdits := []string{"⏰ Pay rent\nSnoozed until", "✅ Pay rent", "✅ Read", "⏭️ Read skipped", "🗑️ Deleted task"}
	if len(fake.edited) != len(wantEdits) {
		t.Fatalf("edits = %q", fake.edited)
	}
	for i, want := range wantEdits {
		if !strings.HasPrefix(fake.edited[i], want) {
			t.Errorf("edit %d = %q, want %q", i, fake.edited[i], want)
		}
	}
	if last := fake.answers[len(fake.answers)-1]; last != "This bot is private" {
		t.Errorf("stranger's tap answered %q", last)
	}
	history, _ := app.Store.History(ctx, 42)
	if len(history) != 4 || !strings.Contains(history[1].Content[0].Text, "marked task t1") {
		t.Fatalf("notes = %+v", history)
	}
}

func TestServesBuiltAssets(t *testing.T) {
	app, _ := testApp(t)
	res := newBrowser(t, app.Handler()).do("GET", "/assets/main-abc123.css", nil)
	if res.Code != http.StatusOK || res.Body.String() != "body{}" {
		t.Fatalf("asset: %d %q", res.Code, res.Body.String())
	}
}

func TestFavicon(t *testing.T) {
	app, _ := testApp(t)
	b := newBrowser(t, app.Handler())
	res := b.do("GET", "/favicon.png", nil)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(res.Body.String(), "\x89PNG") {
		t.Fatalf("favicon: %d %s", res.Code, res.Header().Get("Content-Type"))
	}
	if body := b.page("/login"); !strings.Contains(body, `href="/favicon.png"`) {
		t.Fatal("pages don't link the favicon")
	}
}

func TestPageErrorRendersErrorPage(t *testing.T) {
	app, _ := testApp(t)
	res := httptest.NewRecorder()
	app.handlePageError(res, httptest.NewRequest("GET", "/", nil), httperrors.New("no such thing", http.StatusNotFound))
	if body := res.Body.String(); res.Code != http.StatusNotFound || !strings.Contains(body, "no such thing") {
		t.Fatalf("got %d %q", res.Code, body)
	}
	res = httptest.NewRecorder()
	app.handlePageError(res, httptest.NewRequest("GET", "/", nil), errors.New("redis: secret detail"))
	if body := res.Body.String(); res.Code != http.StatusInternalServerError || strings.Contains(body, "secret detail") {
		t.Fatalf("got %d %q", res.Code, body)
	}
}
