// local runs Task Manager on this machine: workflows run in process on a
// local World in .workflows, and updates come from long polling instead of a
// webhook, so the setup guide skips connecting one. It needs REDIS_URL and
// SETUP_PASSWORD, and AI_GATEWAY_API_KEY or VERCEL_OIDC_TOKEN from vercel env
// pull. The bot's token is saved from the setup page, and polling starts as
// soon as it is.
//
// With -dev, pages load their assets from the Vite dev server (`pnpm dev`);
// cmd/dev starts both. Without it, they use the embedded build of
// `pnpm build`.
//
// Long polling turns the bot's webhook off. Connect it again from the
// deployment's setup page afterwards.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eriicafes/go-workflow"
	localworld "github.com/eriicafes/go-workflow/worlds/local"
	"github.com/eriicafes/httpx"
	"github.com/eriicafes/tmpl/vite"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
	"github.com/eriicafes/workflow-taskmanager/lib/telegram"
	"github.com/eriicafes/workflow-taskmanager/web"
	"github.com/eriicafes/workflow-taskmanager/workflows/flows"
)

func main() {
	port := cmp.Or(os.Getenv("PORT"), "8100")
	dev := flag.Bool("dev", false, "load assets from the Vite dev server")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !flows.HasGatewayToken() {
		log.Fatal("set AI_GATEWAY_API_KEY, or run vercel env pull for VERCEL_OIDC_TOKEN")
	}
	s, err := store.Open()
	if err != nil {
		log.Fatal(err)
	}
	f, err := flows.New(s)
	if err != nil {
		log.Fatal(err)
	}
	runners := f.Runners()

	world, err := localworld.Open(localworld.Config{
		Dir:     ".workflows",
		BaseURL: "http://localhost:" + port + "/v1/workflow",
	}, runners)
	if err != nil {
		log.Fatal(err)
	}
	defer world.Close()
	if err := prepare(ctx, world); err != nil {
		log.Fatal(err)
	}

	var v *vite.Vite
	if *dev {
		v, err = vite.New(vite.Config{Dev: true})
	} else {
		v, err = web.BuiltVite()
	}
	if err != nil {
		log.Fatal(err)
	}
	app, err := web.NewApp(s, v)
	if err != nil {
		log.Fatal(err)
	}
	// Runs are recorded in the local World instead of the Vercel World, and
	// the Workflow SDK web UI can inspect them through /v1/workflow. Its RPC
	// endpoint is open because the server listens only on localhost.
	app.Client = workflow.NewClient(world)
	app.Workflow = localworld.HTTP(world)
	app.Polling = true

	// Restart the agenda chains a new or wiped .workflows lost.
	users, err := s.Users(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, chatID := range users {
		if err := app.EnsureAgenda(ctx, chatID); err != nil {
			log.Printf("agenda for %d: %v", chatID, err)
		}
	}
	go poll(ctx, app)

	log.Printf("Task Manager setup page at http://localhost:%s", port)
	if err := httpx.ListenAndServe("localhost:"+port, app.Handler(), nil); err != nil {
		log.Print(err)
	}
}

// prepare prepares world, waiting while a previous process that cmd/dev is
// replacing still holds .workflows.
func prepare(ctx context.Context, world *localworld.World) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := world.Prepare(ctx)
		if !errors.Is(err, localworld.ErrInUse) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// poll long polls for the bot the setup page saved. It waits while there is
// none, and switches to a new one when its token changes, turning off that
// bot's webhook so polling works.
func poll(ctx context.Context, app *web.App) {
	var token string
	var offset int64
	wait := func(d time.Duration) {
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}
	for ctx.Err() == nil {
		bot, err := app.Store.Bot(ctx)
		if errors.Is(err, store.ErrNoBot) {
			token = ""
			wait(2 * time.Second)
			continue
		} else if err != nil {
			log.Printf("load bot: %v", err)
			wait(3 * time.Second)
			continue
		}
		client := telegram.New(bot.Token)
		if bot.Token != token {
			if err := client.DeleteWebhook(ctx); err != nil {
				log.Printf("delete webhook of @%s: %v", bot.Username, err)
				wait(3 * time.Second)
				continue
			}
			token, offset = bot.Token, 0
			log.Printf("polling @%s for updates", bot.Username)
		}
		// A short timeout notices a new token from the setup page quickly.
		updates, err := client.GetUpdates(ctx, offset, 10)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("get updates: %v", err)
				wait(3 * time.Second)
			}
			continue
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			if err := app.HandleUpdate(ctx, update); err != nil {
				log.Printf("update %d: %v", update.UpdateID, err)
			}
		}
	}
}
