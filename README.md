# Task Manager ✅

A Telegram assistant for what you have coming up. Tell it what you need to do
and when, and it keeps the list, reminds you at the right time with Done and
Snooze buttons, and sends you the day's agenda each morning. It also helps you
build habits: reminders daily at set times ("meds at 8 am and 8 pm"), or every
few hours within a window ("water every 2 hours, 9 to 9"), on every day or some
weekdays, with Done and Skip buttons and streaks.

It is built like [LoveLoop](https://github.com/eriicafes/loveloop): a Go app on Vercel on
[go-workflow](https://github.com/eriicafes/go-workflow), where every
conversation, reminder and agenda is a durable run, and the agent is a
[`workflowai`](https://github.com/eriicafes/go-workflow/tree/main/workflowai)
`WorkflowAgent` on [Go AI SDK](https://goaisdk.com).

## Setting up the bot

There is no bot token in the environment. Sign in to the deployment's setup
page and it guides you through four steps, showing each as it's done:

1. **Create a bot with @BotFather**: `/newbot`, a name, a username ending in
   `bot`.
2. **Paste its token.** Task Manager checks it with Telegram's `getMe`, saves
   it in Redis with a freshly generated webhook secret, and sets the bot's
   command menu. The page only ever shows the bot ID part of the token.
3. **Connect it**: point Telegram's webhook at `<URL>/telegram`. Running
   locally with `cmd/local`, this step is done for you: it long polls instead.
4. **Allow your Telegram account.** The bot only answers allowed Telegram user
   IDs. Message the bot and it replies with your ID, and you appear on the
   setup page with an Allow button (the page refreshes itself while it
   waits). Or get your ID from @userinfobot, or `/id` to the bot, and paste
   it in.

Once all four are done, `/` is the dashboard: the bot's delivery status, who
may use it (with access requests, and adding by ID), and each user's tasks and
habits. `/?guide=1` shows the guide again, and "Use a different bot" goes back
to step 2.

## How it works

| Workflow | Started by | What it does |
| --- | --- | --- |
| `Conversation` | The first message in an empty mailbox | Answers the user's mailbox in order: messages sent in a row are one turn. Each turn loads the user, their tasks, habits, summary and recent history, runs the agent, saves the new messages, replies, then folds old history into the summary. Ends after 10 idle minutes or 10 turns. |
| `Reminder` | `addTask`, `updateTask`, `addHabit`, `updateHabit`, the Snooze button, then itself | Sleeps toward the reminder's time for at most 6 days, then starts the next run toward it. Once due, sends the reminder with buttons. A habit's run then starts the run for its next reminder. A run whose task or habit is gone, done, or has a newer `Version` ends without sending, so rescheduling and deleting need no cancelling. |
| `Agenda` | A user's first message, then itself every day | Sleeps until the user's agenda hour (8 am by default) and sends the day's overdue and due tasks, habits, and what's coming up this week, then starts the next day's run. Changing the agenda hour or time zone starts a new chain; the old one ends when it sees the store records another run. |

The agent's tools: `addTask`, `updateTask` (including marking done),
`deleteTask`, `addHabit`, `updateHabit`, `logHabit`, `deleteHabit`, and
`updateSettings` (name, time zone, agenda hour, pausing habit reminders).
Tasks and habits get short IDs such as `t4` and `h2` that the model repeats
easily and never shows the user.

Some things skip the agent so they're instant and free: `/today`, `/tasks`,
`/habits`, `/pause`, `/resume`, `/id` and `/reset` answer directly, and
reminder buttons update the store and edit the reminder in place. Reminders,
the agenda and button taps leave a note in the conversation, so when the user
next writes, the agent knows what it sent and what they tapped.

Messages go to the user's mailbox in Redis and wake their `Conversation` run
through a hook, or start one when none is listening, as in LoveLoop. No wait
in any workflow is longer than six days, under the seven days Vercel Queues
delays, and every workflow embeds `workflow.StartOnLatest`, so runs move to
new deployments within days.

Data lives in Redis under `taskmanager:*`: the bot, allowed users and access
requests, and per user their settings, tasks, habits, and the last messages of
their conversation. The workflow World uses the same Redis under
`go-workflow:*`. Done tasks are kept up to 30 per user.

```
api/          Vercel functions: index (every public route) and queue (workflow consumer)
web/          App: httpx routes, one file per path (index.go, setup_token.go, telegram.go, ...)
web/templates tmpl pages (struct beside its html) and autoloaded components: the guide's steps and the ui kit
web/src       Tailwind entry; Vite (package.json and vite.config.ts at the root) builds it into web/dist, which web embeds
workflows/    Workflow types and the World; flows/ holds Flows: the runners, agent, and tools
lib/store     Redis storage, with habit schedules and streaks; storetest runs it on an in-memory Redis
lib/agenda    The plain-text agenda and task and habit lists
lib/telegram  Bot API client
cmd/local     Run everything locally with long polling and a local World
cmd/dev       Run cmd/local -dev and the Vite dev server with live reload (go-dev)
```

## Run locally

Needs Go 1.26, Node with pnpm, Redis, and the Vercel CLI with this directory
linked to a project (`vercel link`) for the AI Gateway's OIDC token, or an
`AI_GATEWAY_API_KEY`.

```sh
docker run -d -p 6379:6379 redis
pnpm install
vercel env pull
set -a; source .env.local; set +a
export REDIS_URL=redis://localhost:6379 SETUP_PASSWORD=pick-one
go run ./cmd/dev
```

Open http://localhost:8100, sign in, and follow the guide. Polling starts as
soon as the token is saved. Polling removes the bot's webhook, so if the same
bot is also deployed, connect it again from the deployment's setup page.
Without Vite, build the assets once with `pnpm build` and run
`go run ./cmd/local`.

## Deploy to Vercel

1. Create the project, add the Upstash Redis integration, and enable Vercel
   Queues.
2. Set `SETUP_PASSWORD`, and optionally `MODEL`. For the Workflow SDK web UI
   at `/_workflow`, also set `WORKFLOW_HTTP_TOKEN`,
   `WORKFLOW_TARGET_WORLD=go-workflow/world-http`, and
   `WORKFLOW_HTTP_BASE_URL` to your production domain plus `/v1/workflow`.
3. Deploy, open the deployment's URL, sign in, and follow the guide.

`vercel.json`, `api/workflow-web.ts` and the asset build work as in LoveLoop:
the queue function subscribes to the `taskmanager-workflow` topic, every other
path goes to `api/index.go`, and `/_workflow` only answers browsers signed in
to the setup page.

The bot token sits in Redis in plain text, as it would in an environment
variable; anyone with access to the Redis database can control the bot.

## Development

```sh
go test ./...
dir=$(go list -m -f '{{.Dir}}' github.com/eriicafes/go-workflow)
(cd "$dir" && go build -o /tmp/workflowcheck ./cmd/workflowcheck)
go vet -vettool=/tmp/workflowcheck ./...
```

Tests need no Redis: they run the store on miniredis. The web tests walk the
whole setup guide against a fake Telegram, and the flows tests run the
`Reminder` and `Agenda` workflows on a local World.
