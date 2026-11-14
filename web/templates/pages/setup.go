package pages

import "github.com/eriicafes/workflow-taskmanager/lib/telegram"

// Setup is how far setting up the bot has come, which both the guide and
// the dashboard show.
type Setup struct {
	// StoreError explains why the store can't be used, such as REDIS_URL
	// not being set; nothing else can be set up until it is fixed.
	StoreError string
	// Bot is the saved bot, empty until a token is saved.
	Bot Bot
	// BotError explains why Telegram rejected the saved token.
	BotError   string
	Webhook    telegram.WebhookInfo
	WebhookURL string
	// Connected reports whether Telegram delivers to WebhookURL.
	Connected bool
	LastError string
	PublicURL string
	// Polling reports that this server long polls for updates, so it needs
	// no webhook.
	Polling  bool
	Allowed  []AllowedUser
	Requests []Request
}

// Bot is the saved bot as the page shows it.
type Bot struct {
	Username string
	Name     string
	// TokenHint shows the token's bot ID, never its secret.
	TokenHint string
}

// AllowedUser is a Telegram user allowed to use the bot.
type AllowedUser struct {
	ID int64
	// Name is empty until they first message the bot.
	Name string
}

// Request is someone who messaged the bot without being allowed to.
type Request struct {
	ID       int64
	Name     string
	Username string
	When     string
}

// Steps of the setup guide.
const (
	StepCreate = iota + 1
	StepToken
	StepWebhook
	StepAccess
	steps = StepAccess
)

// Done reports whether step is done.
func (s Setup) Done(step int) bool {
	if s.StoreError != "" {
		return false
	}
	switch step {
	case StepCreate:
		return s.Bot.TokenHint != ""
	case StepToken:
		return s.Bot.TokenHint != "" && s.BotError == ""
	case StepWebhook:
		return s.Done(StepToken) && (s.Polling || s.Webhook.URL != "")
	case StepAccess:
		return len(s.Allowed) > 0
	}
	return false
}

// State is a step's state in the guide: "done", "open" when it is the
// step to do now, or "locked" until earlier steps are done. Creating the bot
// and saving its token are open together, since the guide can't tell the
// bot was created until its token is saved.
func (s Setup) State(step int) string {
	if s.Done(step) {
		return "done"
	}
	for earlier := StepToken; earlier < step; earlier++ {
		if !s.Done(earlier) {
			return "locked"
		}
	}
	return "open"
}

// Complete reports whether every step is done, so the bot is ready to use.
func (s Setup) Complete() bool {
	for step := 1; step <= steps; step++ {
		if !s.Done(step) {
			return false
		}
	}
	return true
}

// DoneCount returns how many steps are done.
func (s Setup) DoneCount() int {
	n := 0
	for step := 1; step <= steps; step++ {
		if s.Done(step) {
			n++
		}
	}
	return n
}

// Steps returns how many steps the guide has.
func (s Setup) Steps() int { return steps }

// Percent returns how much of the setup is done, for the progress bar.
func (s Setup) Percent() int { return s.DoneCount() * 100 / steps }

// Waiting reports whether the guide waits for the owner to message the bot,
// so the page reloads itself to show them when they do.
func (s Setup) Waiting() bool {
	return s.State(StepAccess) == "open" && len(s.Requests) == 0
}
