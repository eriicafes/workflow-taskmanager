package web

import (
	_ "embed"
	"net/http"
)

// favicon is the app's logo, a white check mark in a black square, at
// 180px, which also suits iOS home screen icons.
//
//go:embed favicon.png
var favicon []byte

// Favicon serves GET /favicon.png.
func (a *App) Favicon(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, err := w.Write(favicon)
	return err
}
