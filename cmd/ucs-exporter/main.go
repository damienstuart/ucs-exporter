// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Command ucs-exporter is a Prometheus exporter for Cisco UCS Manager.
//
// It is a Go rewrite of prometheus-ucs-exporter by Marshall Wace, itself a
// fork of Drew Stinnett's exporter at Duke University OIT.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

const defaultListen = ":3001"

func main() {
	app := kingpin.New("ucs-exporter", "Prometheus exporter for Cisco UCS Manager.").DefaultEnvars()
	app.Version(version.Print("ucs-exporter"))
	app.HelpFlag.Short('h')

	configFile := app.Flag("config.file", "Configuration file.").Default("/etc/ucs-exporter/config.yml").String()
	logConfig := &promslog.Config{}
	flag.AddFlags(app, logConfig)

	serveCmd := app.Command("serve", "Poll the configured UCS domains and serve metrics (default).").Default()
	webFlags := kingpinflag.AddFlags(serveCmd, defaultListen)
	lifecycle := serveCmd.Flag("web.enable-lifecycle", "Enable POST /-/reload.").Bool()

	checkCmd := app.Command("check-config", "Validate the configuration file and exit.")

	modulesCmd := app.Command("modules", "List metric modules and the metrics they export.")
	markdown := modulesCmd.Flag("markdown", "Print a Markdown metrics reference.").Bool()

	ex := newExplore(app, configFile)

	cmd := kingpin.MustParse(app.Parse(os.Args[1:]))
	logger := promslog.New(logConfig)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case serveCmd.FullCommand():
		err = runServe(ctx, logger, *configFile, webFlags, *lifecycle)
	case checkCmd.FullCommand():
		err = runCheck(os.Stdout, *configFile)
	case modulesCmd.FullCommand():
		err = runModules(os.Stdout, *markdown)
	default:
		err = ex.run(ctx, cmd, logger)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ucs-exporter:", err)
		os.Exit(1)
	}
}
