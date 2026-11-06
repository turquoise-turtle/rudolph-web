// Command local runs rudolph-web as a normal HTTP server with the in-memory
// demo store, for development.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/config"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	ac := cfg.AuthConfig()
	ac.Logger = log
	a, err := auth.New(ac)
	if err != nil {
		log.Error("auth setup", "err", err)
		os.Exit(1)
	}
	h, err := web.New(web.Options{Auth: a, Store: fake.NewDemo(time.Now()), Logger: log, CLIEnv: cfg.CLIEnv})
	if err != nil {
		log.Error("web setup", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("listening (demo data)", "addr", "http://"+cfg.Addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
