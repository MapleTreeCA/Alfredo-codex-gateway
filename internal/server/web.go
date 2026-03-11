package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webAssets embed.FS

var staticHandler = mustStaticHandler()

func mustStaticHandler() http.Handler {
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

func (s *Server) HandleWeb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	staticHandler.ServeHTTP(w, r)
}
