// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Derived from scripts/explore.py of prometheus-ucs-exporter,
// (c) 2022 Marshall Wace, GPL-3.0-only.

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	promcfg "github.com/prometheus/common/config"
	"github.com/prometheus/common/version"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/modules"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// explore implements the "explore" commands, which talk to a UCS Manager
// directly: for troubleshooting, finding class and attribute names, and
// capturing test fixtures.
type explore struct {
	configFile *string

	domain       *string
	address      *string
	username     *string
	passwordFile *string
	insecure     *bool
	caFile       *string
	serverName   *string
	timeout      *time.Duration
	debugXML     *bool

	login *kingpin.CmdClause

	query      *kingpin.CmdClause
	queryClass *string
	filters    *[]string
	hier       *bool
	output     *string

	dnCmd  *kingpin.CmdClause
	dnArg  *string
	dnHier *bool
	dnOut  *string

	children      *kingpin.CmdClause
	childrenDN    *string
	childrenClass *string
	childrenOut   *string

	classes   *kingpin.CmdClause
	namesOnly *bool

	capture *captureOptions
}

func newExplore(app *kingpin.Application, configFile *string) *explore {
	ex := &explore{configFile: configFile}
	c := app.Command("explore", "Query a UCS Manager directly: troubleshooting, class discovery and fixture capture.")
	ex.domain = c.Flag("domain", "Use the settings of this domain from --config.file.").String()
	ex.address = c.Flag("address", "UCS Manager address (default $PROM_UCS_DOMAIN).").Envar("PROM_UCS_DOMAIN").String()
	ex.username = c.Flag("username", "Username (default $PROM_UCS_USERNAME).").Envar(config.EnvUsername).String()
	ex.passwordFile = c.Flag("password-file", "File containing the password (default: $PROM_UCS_PASSWORD).").String()
	ex.insecure = c.Flag("tls.insecure-skip-verify", "Do not verify the UCS Manager certificate.").Bool()
	ex.caFile = c.Flag("tls.ca-file", "CA certificate to verify UCS Manager with.").String()
	ex.serverName = c.Flag("tls.server-name", "Expected certificate server name.").String()
	ex.timeout = c.Flag("timeout", "Request timeout.").Default("120s").Duration()
	ex.debugXML = c.Flag("debug-xml", "Print requests and responses (passwords and cookies redacted) to stderr.").Bool()

	ex.login = c.Command("login", "Log in and print session details.")

	ex.query = c.Command("query", "Print all objects of a class (configResolveClass).")
	ex.queryClass = ex.query.Arg("class", "Class ID, e.g. fcErrStats.").Required().String()
	ex.filters = ex.query.Flag("filter", "Filter such as 'severity!=cleared' or 'dn~^sys/chassis-1/' (repeatable, ANDed).").Strings()
	ex.hier = ex.query.Flag("hierarchical", "Include child objects.").Bool()
	ex.output = ex.query.Flag("output", "Output format: text, json or xml.").Short('o').Default("text").Enum("text", "json", "xml")

	ex.dnCmd = c.Command("dn", "Print the object with a DN (configResolveDn).")
	ex.dnArg = ex.dnCmd.Arg("dn", "Distinguished name, e.g. sys/chassis-1.").Required().String()
	ex.dnHier = ex.dnCmd.Flag("hierarchical", "Include child objects.").Bool()
	ex.dnOut = ex.dnCmd.Flag("output", "Output format: text, json or xml.").Short('o').Default("text").Enum("text", "json", "xml")

	ex.children = c.Command("children", "Print the children of a DN (configResolveChildren).")
	ex.childrenDN = ex.children.Arg("dn", "Parent distinguished name.").Required().String()
	ex.childrenClass = ex.children.Flag("class", "Only children of this class.").String()
	ex.childrenOut = ex.children.Flag("output", "Output format: text, json or xml.").Short('o').Default("text").Enum("text", "json", "xml")

	ex.classes = c.Command("classes", "List the classes and attributes the modules query (offline).")
	ex.namesOnly = ex.classes.Flag("names-only", "Print only class IDs.").Bool()

	ex.capture = addCapture(c)
	return ex
}

func (ex *explore) run(ctx context.Context, cmd string, logger *slog.Logger) error {
	if cmd == ex.classes.FullCommand() {
		return ex.runClasses(os.Stdout)
	}
	sess, name, err := ex.connect(logger)
	if err != nil {
		return err
	}
	defer func() {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = sess.Logout(lctx)
	}()
	rctx, cancel := context.WithTimeout(ctx, *ex.timeout)
	defer cancel()
	if err := sess.Ensure(rctx); err != nil {
		return err
	}

	switch cmd {
	case ex.login.FullCommand():
		info := sess.Info()
		fmt.Printf("address:        %s\nversion:        %s\nprivileges:     %s\nrefresh period: %s\n",
			sess.Client().URL(), info.Version, info.Priv, info.RefreshPeriod)
		return nil
	case ex.query.FullCommand():
		var f ucsm.Filter
		var fs []ucsm.Filter
		for _, expr := range *ex.filters {
			pf, err := ucsm.ParseFilterExpr(*ex.queryClass, expr)
			if err != nil {
				return err
			}
			fs = append(fs, pf)
		}
		switch len(fs) {
		case 0:
		case 1:
			f = fs[0]
		default:
			f = ucsm.And(fs...)
		}
		build := func(c string) ucsm.Request { return ucsm.ResolveClassRequest(c, *ex.queryClass, f, *ex.hier) }
		return ex.print(rctx, sess, build, *ex.output)
	case ex.dnCmd.FullCommand():
		build := func(c string) ucsm.Request { return ucsm.ResolveDnRequest(c, *ex.dnArg, *ex.dnHier) }
		return ex.print(rctx, sess, build, *ex.dnOut)
	case ex.children.FullCommand():
		build := func(c string) ucsm.Request {
			return ucsm.ResolveChildrenRequest(c, *ex.childrenDN, *ex.childrenClass, nil, false)
		}
		return ex.print(rctx, sess, build, *ex.childrenOut)
	case ex.capture.cmd.FullCommand():
		return ex.capture.run(ctx, sess, name, *ex.timeout)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// connect builds a session from a configured domain or the explore flags.
func (ex *explore) connect(logger *slog.Logger) (*ucsm.Session, string, error) {
	var (
		address, name string
		creds         ucsm.Credentials
		tlsCfg        *tls.Config
		err           error
	)
	if *ex.domain != "" {
		cfg, err := loadConfig(*ex.configFile, logger)
		if err != nil {
			return nil, "", err
		}
		r, ok := cfg.Lookup(*ex.domain)
		if !ok {
			if r, err = cfg.ResolveUnlisted(*ex.domain); err != nil {
				return nil, "", fmt.Errorf("domain %q is not in %s", *ex.domain, *ex.configFile)
			}
		}
		address, name, creds = r.Address, r.Name, r.Credentials()
		if tlsCfg, err = r.TLSClientConfig(); err != nil {
			return nil, "", err
		}
	} else {
		if *ex.address == "" {
			return nil, "", errors.New("set --domain, or --address (or $PROM_UCS_DOMAIN)")
		}
		if *ex.username == "" {
			return nil, "", fmt.Errorf("set --username or $%s", config.EnvUsername)
		}
		password := os.Getenv(config.EnvPassword)
		if *ex.passwordFile != "" {
			b, err := os.ReadFile(*ex.passwordFile)
			if err != nil {
				return nil, "", err
			}
			password = strings.TrimRight(string(b), "\r\n")
		}
		if password == "" {
			return nil, "", fmt.Errorf("set --password-file or $%s", config.EnvPassword)
		}
		address, name = *ex.address, *ex.address
		creds = ucsm.StaticCredentials{User: *ex.username, Password: password}
		tc := promcfg.TLSConfig{InsecureSkipVerify: *ex.insecure, CAFile: *ex.caFile, ServerName: *ex.serverName}
		if tlsCfg, err = promcfg.NewTLSConfig(&tc); err != nil {
			return nil, "", err
		}
	}
	cc := ucsm.ClientConfig{Address: address, TLS: tlsCfg, UserAgent: "ucs-exporter/" + version.Version}
	if *ex.debugXML {
		cc.Trace = func(dir, method string, body []byte) {
			fmt.Fprintf(os.Stderr, "--- %s %s\n%s\n", dir, method, body)
		}
	}
	client, err := ucsm.NewClient(cc)
	if err != nil {
		return nil, "", err
	}
	return ucsm.NewSession(client, creds, ucsm.SessionOptions{Logger: logger, BackoffMin: time.Nanosecond}), name, nil
}

func (ex *explore) print(ctx context.Context, sess *ucsm.Session, build func(string) ucsm.Request, format string) error {
	if format == "xml" {
		raw, err := sess.Raw(ctx, build)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(ucsm.Redact(raw), '\n'))
		return err
	}
	res, err := sess.Do(ctx, build, ucsm.DecodeOptions{})
	if err != nil {
		return err
	}
	return printObjects(os.Stdout, res.Objects, format)
}

func printObjects(w io.Writer, mos []*ucsm.MO, format string) error {
	if format == "json" {
		type obj struct {
			Class string            `json:"class"`
			DN    string            `json:"dn"`
			Attrs map[string]string `json:"attrs"`
		}
		out := make([]obj, 0, len(mos))
		for _, mo := range mos {
			o := obj{Class: mo.Class, DN: mo.DN, Attrs: map[string]string{}}
			for _, a := range mo.Attrs {
				o.Attrs[a.Name] = a.Value
			}
			out = append(out, o)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, mo := range mos {
		fmt.Fprintf(w, "%s %s\n", mo.Class, mo.DN)
		attrs := append([]ucsm.Attr(nil), mo.Attrs...)
		sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
		for _, a := range attrs {
			fmt.Fprintf(w, "    %-32s %s\n", a.Name, a.Value)
		}
	}
	fmt.Fprintf(w, "%d objects\n", len(mos))
	return nil
}

func allModules() []module.Module {
	return modules.Builtin(config.Options{VirtualVLANs: "full", IOMHostPorts: true, AdaptorUplinkErrors: true, FirmwareTypes: config.DefaultFirmwareTypes})
}

func (ex *explore) runClasses(w io.Writer) error {
	queries := module.MergeQueries(allModules())
	users := map[string][]string{}
	for _, m := range allModules() {
		for _, q := range m.Queries() {
			users[q.Class] = append(users[q.Class], m.Name())
		}
	}
	for _, q := range queries {
		if *ex.namesOnly {
			fmt.Fprintln(w, q.Class)
			continue
		}
		fmt.Fprintf(w, "%s  (modules: %s)\n", q.Class, strings.Join(users[q.Class], ", "))
		if q.Filter != nil {
			fmt.Fprintf(w, "    filter: %s\n", q.Filter)
		}
		fmt.Fprintf(w, "    attrs:  %s\n", strings.Join(q.Attrs, " "))
	}
	return nil
}
