package pages

import "github.com/eriicafes/tmpl"

// Guide walks the owner through setting up the bot, one step at a time.
type Guide struct {
	Setup Setup
	Flash Flash
}

func (p Guide) Tmpl() tmpl.Template {
	return tmpl.Wrap(Layout{Title: "Set up · Task Manager"}, tmpl.Tmpl("pages/guide", p))
}
