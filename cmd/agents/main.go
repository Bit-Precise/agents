package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/bit-precise/agentd/credentials"
	"github.com/bit-precise/agents/control"
	"github.com/bit-precise/agents/control/assets"
	"github.com/bit-precise/agents/edge"
	"github.com/bit-precise/agents/gateway"
	"github.com/rs/zerolog"
	"golang.org/x/net/netutil"
	"gopkg.in/yaml.v3"
)

var Version = "dev"

func main() {
	log := zerolog.New(os.Stderr).With().Timestamp().Logger()
	if err := run(os.Args[1:], os.Stdout, log); err != nil {
		log.Error().Err(err).Msg("agents stopped")
		os.Exit(1)
	}
}
func run(args []string, out io.Writer, log zerolog.Logger) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprintln(out, "agents init --config agents.yaml\nagents serve --config agents.yaml\nagents control --config agents.yaml\nagents edge --config agents.yaml\nagents console --config agents.yaml\nagents version")
		return err
	}
	if args[0] == "version" || args[0] == "--version" {
		_, err := fmt.Fprintln(out, Version)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("config", "agents.yaml", "server configuration")
	var listen, serverURL, edgeURL, stateDir, certFile, keyFile *string
	var development *bool
	if command == "init" {
		listen = fs.String("listen", "127.0.0.1:8080", "listener")
		serverURL = fs.String("server-url", "http://localhost:8080", "console/control origin")
		edgeURL = fs.String("edge-url", "", "public XHTTP endpoint")
		stateDir = fs.String("state-dir", "./state/agents", "private state directory")
		development = fs.Bool("development-http", true, "loopback-only HTTP development mode")
		certFile = fs.String("tls-cert", "", "TLS certificate")
		keyFile = fs.String("tls-key", "", "TLS private key")
	}
	if command != "init" && command != "serve" && command != "run" && command != "control" && command != "edge" && command != "console" {
		return errors.New("unknown agents command")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if command == "init" {
		cfg := control.Config{ListenAddress: *listen, ServerURL: *serverURL, EdgeURL: *edgeURL, StateDir: *stateDir, DevelopmentHTTP: *development}
		if cfg.EdgeURL == "" {
			cfg.EdgeURL = cfg.ServerURL + "/agent"
		}
		cfg.Database.Type = "sqlite"
		cfg.TLS.CertFile = *certFile
		cfg.TLS.KeyFile = *keyFile
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		base := filepath.Dir(absolute)
		dataDir := cfg.StateDir
		if !filepath.IsAbs(dataDir) {
			dataDir = filepath.Join(base, dataDir)
		}
		cfg.AdminTokenFile = filepath.Join(cfg.StateDir, "admin.token")
		cfg.Database.DSN = filepath.Join(cfg.StateDir, "agents.db")
		if err := cfg.Validate(); err != nil {
			return err
		}
		if _, err := os.Lstat(absolute); !os.IsNotExist(err) {
			return errors.New("configuration already exists or cannot be inspected")
		}
		unlock, err := credentials.Lock(dataDir)
		if err != nil {
			return err
		}
		defer unlock()
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return err
		}
		tokenPath := filepath.Join(dataDir, "admin.token")
		f, err := os.OpenFile(tokenPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(hex.EncodeToString(secret[:]) + "\n")
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(base, 0700); err != nil {
			return err
		}
		f, err = os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		closeErr = f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = fmt.Fprintf(out, "Configuration: %s\nAdmin token file: %s\nConsole: %s/console/\n", absolute, tokenPath, cfg.ServerURL)
		return err
	}
	cfg, err := control.LoadConfigFor(*path, command)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer cancel()
	var handler http.Handler
	closeApp := func() error { return nil }
	switch command {
	case "serve", "run", "control":
		token, err := control.ReadSecret(cfg.AdminTokenFile)
		if err != nil {
			return fmt.Errorf("read admin token: %w", err)
		}
		unlock, err := credentials.Lock(cfg.StateDir)
		if err != nil {
			return err
		}
		defer unlock()
		var app *control.App
		if command == "control" {
			internal, err := control.ReadSecret(cfg.InternalTokenFile)
			if err != nil {
				return fmt.Errorf("read internal token: %w", err)
			}
			app, err = control.NewControl(ctx, cfg, token, internal)
			if err != nil {
				return err
			}
		} else {
			app, err = control.New(ctx, cfg, token)
			if err != nil {
				return err
			}
		}
		handler, closeApp = app.Handler(), app.Close
	case "edge":
		internal, err := control.ReadSecret(cfg.InternalTokenFile)
		if err != nil {
			return fmt.Errorf("read internal token: %w", err)
		}
		app, err := gateway.New(ctx, cfg.ControlURL, cfg.EdgeURL, internal, edge.Config{AllowLoopbackHTTP: cfg.DevelopmentHTTP, TLSOffload: cfg.TLSOffload})
		if err != nil {
			return err
		}
		handler, closeApp = app.Handler(), app.Close
	case "console":
		static := assets.Handler()
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte("ok\n"))
				return
			}
			static.ServeHTTP(w, r)
		})
	}
	defer closeApp()
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10, IdleTimeout: 2 * time.Minute}
	result := make(chan error, 1)
	go func() {
		l := netutil.LimitListener(listener, 256)
		if cfg.DevelopmentHTTP || cfg.TLSOffload {
			result <- server.Serve(l)
		} else {
			result <- server.ServeTLS(l, cfg.TLS.CertFile, cfg.TLS.KeyFile)
		}
	}()
	log.Info().Str("listen", listener.Addr().String()).Str("console", cfg.ServerURL+"/console/").Str("mode", command).Msg("agents ready")
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	_ = closeApp()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
