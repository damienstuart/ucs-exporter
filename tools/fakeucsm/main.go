// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Command fakeucsm serves a directory of UCSM fixtures (as written by
// "ucs-exporter explore capture") over the UCSM XML API, for local
// development and end-to-end testing without UCS hardware.
//
//	go run ./tools/fakeucsm -fixtures testdata/fixtures/synthetic -listen 127.0.0.1:8443 -tls -cert-out /tmp/fakeucsm.pem
package main

import (
	"encoding/pem"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"

	"github.com/damienstuart/ucs-exporter/internal/ucsm/ucsmtest"
)

func main() {
	var (
		fixtures = flag.String("fixtures", "testdata/fixtures/synthetic", "Fixture directory.")
		listen   = flag.String("listen", "127.0.0.1:8080", "Listen address.")
		user     = flag.String("user", "admin", "Accepted username.")
		password = flag.String("password", "password", "Accepted password.")
		refresh  = flag.Int("refresh-period", 600, "Session refresh period in seconds.")
		useTLS   = flag.Bool("tls", false, "Serve HTTPS with a self-signed certificate.")
		certOut  = flag.String("cert-out", "", "With -tls, write the certificate (PEM) here for use as tls.ca_file.")
	)
	flag.Parse()

	srv, err := ucsmtest.New(ucsmtest.WithFixtures(*fixtures), ucsmtest.WithUser(*user, *password), ucsmtest.WithRefreshPeriod(*refresh))
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/nuova", srv)
	hs := httptest.NewUnstartedServer(mux)
	hs.Listener.Close()
	hs.Listener = ln
	if *useTLS {
		hs.StartTLS()
		if *certOut != "" {
			b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hs.Certificate().Raw})
			if err := os.WriteFile(*certOut, b, 0o644); err != nil {
				log.Fatal(err)
			}
		}
	} else {
		hs.Start()
	}
	log.Printf("fake UCSM serving %s at %s/nuova (user %q)", *fixtures, hs.URL, *user)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	hs.Close()
}
