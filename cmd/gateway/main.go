package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/admin"
	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/netproxy"
	"ai-media-gateway/internal/openai"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/server"
	"ai-media-gateway/internal/store"
	"ai-media-gateway/internal/web"

	_ "ai-media-gateway/internal/provider/elevenlabs"
	_ "ai-media-gateway/internal/provider/fal"
	_ "ai-media-gateway/internal/provider/fishaudio"
	_ "ai-media-gateway/internal/provider/heygen"
	_ "ai-media-gateway/internal/provider/mimo"
	_ "ai-media-gateway/internal/provider/muse"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	box := secret.New(cfg.MasterKey)
	clients := netproxy.NewClients(cfg.HTTPTimeout)
	pool := accounts.New(st, box, cfg.Cooldowns, clients, time.Now)
	adminHandler := admin.New(st, box, clients, cfg.DBPath)
	openaiHandler := openai.New(st, pool, cfg.DBPath)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: server.New(server.Deps{
			Config: cfg,
			Store:  st,
			Pool:   pool,
			Admin:  adminHandler,
			OpenAI: openaiHandler,
			Web:    web.Handler(),
		}),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := st.PruneRequestLogs(context.Background(), time.Now().Add(-cfg.LogRetention))
				if err != nil {
					slog.Warn("prune request_logs", "err", err)
				} else if n > 0 {
					slog.Info("pruned request_logs", "rows", n)
				}
			}
		}
	}()

	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	slog.Info("listening", "addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
