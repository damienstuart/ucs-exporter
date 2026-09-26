// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/modules"
	"github.com/damienstuart/ucs-exporter/internal/poller"
	"github.com/damienstuart/ucs-exporter/internal/server"
)

// loadConfig loads and fully validates the configuration.
func loadConfig(path string, logger *slog.Logger) (*config.Config, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	if err := modules.Validate(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if logger != nil {
		for _, w := range cfg.Warnings() {
			logger.Warn("configuration: " + w)
		}
	}
	return cfg, nil
}

func runServe(ctx context.Context, logger *slog.Logger, configFile string, webFlags *web.FlagConfig, lifecycle bool) error {
	logger.Info("starting ucs-exporter", "version", version.Info(), "build_context", version.BuildContext())
	cfg, err := loadConfig(configFile, logger)
	if err != nil {
		return err
	}
	mgr, err := poller.NewManager(ctx, cfg, poller.ManagerOptions{
		Build:     modules.Build,
		Logger:    logger,
		UserAgent: "ucs-exporter/" + version.Version,
	})
	if err != nil {
		return err
	}
	logger.Info("polling domains", "domains", len(cfg.Domains), "unlisted_enabled", cfg.UnlistedDomains.Enabled)

	var (
		srv      *server.Server
		reloadMu sync.Mutex
	)
	reload := func() error {
		reloadMu.Lock()
		defer reloadMu.Unlock()
		cfg, err := loadConfig(configFile, logger)
		if err == nil {
			err = mgr.Apply(cfg)
		}
		srv.ReloadResult(err)
		if err != nil {
			logger.Error("configuration reload failed; keeping the previous configuration", "err", err)
			return err
		}
		logger.Info("configuration reloaded", "domains", len(cfg.Domains))
		return nil
	}
	srv, err = server.New(server.Options{Manager: mgr, Logger: logger, EnableLifecycle: lifecycle, Reload: reload})
	if err != nil {
		mgr.Stop()
		return err
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- web.ListenAndServe(httpSrv, webFlags, logger) }()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	for {
		select {
		case <-hup:
			_ = reload()
		case err := <-serveErr:
			mgr.Stop()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			logger.Info("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = httpSrv.Shutdown(shutdownCtx)
			cancel()
			mgr.Stop() // waits for pollers to log out
			mgr.Wait()
			return nil
		}
	}
}
