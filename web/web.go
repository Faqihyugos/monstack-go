package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html
var Content embed.FS

// Handler returns an http.Handler that serves the embedded web dashboard.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := Content.ReadFile("index.html")
		if err != nil {
			http.Error(w, "dashboard not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}

// SubFS returns the embedded filesystem rooted at subPath, or the root filesystem.
func SubFS(subPath string) (fs.FS, error) {
	if subPath == "" {
		return Content, nil
	}
	return fs.Sub(Content, subPath)
}
