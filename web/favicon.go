package web

import (
	_ "embed"
	"net/http"
)

// favicon is the app's logo, a white check mark in a black square.
//
//go:embed favicon.svg
var favicon []byte

// Favicon serves GET /favicon.svg.
func (a *App) Favicon(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, err := w.Write(favicon)
	return err
}
