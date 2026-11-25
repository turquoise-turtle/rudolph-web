// Command local runs rudolph-web as a normal HTTP server with the in-memory
// demo store, for development.
package main

import (
	"context"
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
	// The demo store is in memory, so editing is always on: admins see the
	// forms, and changes last until the server stops.
	demo := fake.NewDemo(time.Now())
	h, err := web.New(web.Options{Auth: a, Store: demo, Writer: demo, Logger: log, CLIEnv: cfg.CLIEnv, TopListStaleAfter: cfg.TopListStaleAfter, MinSantaVersion: cfg.MinSantaVersion,
		FleetStaleAfter: cfg.FleetStaleAfter, FleetRunner: demoFleetRunner{log}})
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

// demoFleetRunner stands in for Rudolph's fleet Lambda, which the demo has
// none of: the button, its 15-minute guard and its audit entry work, and the
// demo's fleet lists stay as they are.
type demoFleetRunner struct {
	log *slog.Logger
}

func (r demoFleetRunner) Start(context.Context) error {
	r.log.Info("demo: the fleet job would be started here; the demo lists do not change")
	return nil
}
