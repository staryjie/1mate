package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"1mate/internal/api"
	"1mate/internal/audit"
	"1mate/internal/auth"
	"1mate/internal/config"
	"1mate/internal/cryptox"
	"1mate/internal/job"
	"1mate/internal/model"
	"1mate/internal/redact"
	"1mate/internal/service"
	"1mate/internal/store"
	"1mate/internal/version"
	"1mate/internal/webui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	key, created, err := cryptox.LoadOrCreateKey(cfg.MasterKey, filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		return fmt.Errorf("master key: %w", err)
	}
	if created {
		logger.Warn("generated a new master key; back it up, without it stored secrets cannot be decrypted",
			"path", filepath.Join(cfg.DataDir, "master.key"))
	}
	box, err := cryptox.NewBox(key)
	if err != nil {
		return err
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "1mate.db"))
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()

	red := redact.New()
	agentHub := service.NewAgentHub(logger)
	svc := service.New(st, box, red, cfg.DataDir, logger, agentHub)
	jobs := job.NewManager(st, cfg.DataDir, red, cfg.Workers)
	authMgr := auth.NewManager(st, cfg.SessionTTL, cfg.SessionIdle, cfg.SecureCookie)
	auditRec := audit.New(st)

	if n, err := jobs.Recover(); err == nil && n > 0 {
		logger.Warn("marked interrupted jobs from the previous run", "count", n)
	}

	registerHandlers(jobs, svc, logger)

	srv := api.NewServer(logger, st, authMgr, svc, jobs, auditRec, version.Version)
	router := api.Router(srv, webui.FS(), "")

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.RequestLogger(logger)(router),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if cfg.InsecureBind() && !cfg.SecureCookie {
		logger.Warn("listening on a non-loopback address without TLS; put a TLS-terminating reverse proxy in front of 1mate")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go sessionReaper(ctx, authMgr, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("1mate starting", "addr", cfg.Addr, "version", version.Version, "data_dir", cfg.DataDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	jobs.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown timed out", "error", err)
	}
	if !jobs.Wait(10 * time.Second) {
		logger.Warn("some jobs did not finish before shutdown")
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func sessionReaper(ctx context.Context, m *auth.Manager, log *slog.Logger) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := m.Reap(); err == nil && n > 0 {
				log.Debug("removed expired sessions", "count", n)
			}
		}
	}
}

// registerHandlers wires every job kind to the service. Keeping this in main
// makes the queue's capabilities explicit in one place.
func registerHandlers(jobs *job.Manager, svc *service.Service, log *slog.Logger) {
	jobs.Register(model.JobHostProbe, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		return svc.RunProbeJob(ctx, j.HostID, sink.Printf)
	})
	jobs.Register(model.JobInstallDocker, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		p, err := service.ParseInstallPayload(j.Payload)
		if err != nil {
			return err
		}
		return svc.RunInstallJob(ctx, p, sink.Printf)
	})
	jobs.Register(model.JobDeploy, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		return svc.RunDeployJob(ctx, j.Payload, sink.Printf)
	})
	for _, kind := range []model.JobKind{model.JobStart, model.JobStop, model.JobRestart, model.JobReconcile} {
		k := kind
		jobs.Register(k, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
			return svc.RunSimpleAction(ctx, k, j.Payload, sink.Printf)
		})
	}
	jobs.Register(model.JobUpgrade, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		return svc.RunUpgradeJob(ctx, j.Payload, sink.Printf)
	})
	jobs.Register(model.JobDestroy, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		return svc.RunDestroyJob(ctx, j.Payload, sink.Printf)
	})
	jobs.Register(model.JobBackup, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		var p service.BackupPayload
		if err := decodePayload(j.Payload, &p); err != nil {
			return err
		}
		return svc.RunBackupJob(ctx, p, sink.Printf)
	})
	jobs.Register(model.JobRestore, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		var p service.RestorePayload
		if err := decodePayload(j.Payload, &p); err != nil {
			return err
		}
		return svc.RunRestoreJob(ctx, p, sink.Printf)
	})
	jobs.Register(model.JobImageSync, func(ctx context.Context, j *model.Job, sink *job.LogSink) error {
		return svc.RunImageSyncJob(ctx, j.Payload, sink.Printf)
	})
	log.Debug("job handlers registered", "kinds", len(model.AllJobKinds()))
}

func decodePayload(raw string, dst any) error {
	if raw == "" || raw == "{}" {
		return errors.New("job payload is empty")
	}
	return json.Unmarshal([]byte(raw), dst)
}
