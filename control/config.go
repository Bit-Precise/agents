package control

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bit-precise/agentd/policy"
	"github.com/bit-precise/agents/control/db"
	"gopkg.in/yaml.v3"
)

// Config mirrors Headscale's separation of server configuration from agent state.
type Config struct {
	ListenAddress  string `yaml:"listenAddress"`
	ServerURL      string `yaml:"serverURL"`
	EdgeURL        string `yaml:"edgeURL"`
	StateDir       string `yaml:"stateDir"`
	AdminTokenFile string `yaml:"adminTokenFile"`
	Database       struct {
		Type    string `yaml:"type"`
		DSN     string `yaml:"dsn"`
		DSNFile string `yaml:"dsnFile"`
	} `yaml:"database"`
	TLS struct {
		CertFile string `yaml:"certFile"`
		KeyFile  string `yaml:"keyFile"`
	} `yaml:"tls"`
	DevelopmentHTTP   bool       `yaml:"developmentHTTP"`
	TLSOffload        bool       `yaml:"tlsOffload"`
	InternalTokenFile string     `yaml:"internalTokenFile"`
	ControlURL        string     `yaml:"controlURL"`
	EdgeInternalURL   string     `yaml:"edgeInternalURL"`
	ConsoleURL        string     `yaml:"consoleURL"`
	OIDC              OIDCConfig `yaml:"oidc"`
}

type OIDCConfig struct {
	Enabled                bool     `yaml:"enabled"`
	Issuer                 string   `yaml:"issuer"`
	ClientID               string   `yaml:"clientId"`
	ClientSecretFile       string   `yaml:"clientSecretFile"`
	RedirectURL            string   `yaml:"redirectURL"`
	Scopes                 []string `yaml:"scopes"`
	BootstrapAdminSubjects []string `yaml:"bootstrapAdminSubjects"`
}

func LoadConfig(path string) (Config, error) { return LoadConfigFor(path, "serve") }

func LoadConfigFor(path, mode string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return cfg, err
	}
	if len(data) > 1<<20 {
		return cfg, errors.New("server config exceeds 1 MiB")
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err = d.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("server config: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("server config must contain one document")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, err
	}
	if cfg.StateDir == "" {
		cfg.StateDir = "./state/agents"
	}
	cfg.StateDir = resolvePath(base, cfg.StateDir)
	if cfg.AdminTokenFile == "" {
		cfg.AdminTokenFile = filepath.Join(cfg.StateDir, "admin.token")
	} else {
		cfg.AdminTokenFile = resolvePath(base, cfg.AdminTokenFile)
	}
	if cfg.Database.DSNFile != "" {
		cfg.Database.DSNFile = resolvePath(base, cfg.Database.DSNFile)
		if cfg.Database.DSN != "" {
			return cfg, errors.New("database.dsn and database.dsnFile are mutually exclusive")
		}
		if mode != "edge" && mode != "console" {
			cfg.Database.DSN, err = ReadSecret(cfg.Database.DSNFile)
			if err != nil {
				return cfg, fmt.Errorf("read database DSN file: %w", err)
			}
		}
	}
	if cfg.Database.Type == "" {
		cfg.Database.Type = "sqlite"
	}
	if cfg.Database.Type == "sqlite" {
		if cfg.Database.DSN == "" {
			cfg.Database.DSN = filepath.Join(cfg.StateDir, "agents.db")
		} else if cfg.Database.DSN != ":memory:" {
			cfg.Database.DSN = resolvePath(base, cfg.Database.DSN)
		}
	}
	if cfg.TLS.CertFile != "" {
		cfg.TLS.CertFile = resolvePath(base, cfg.TLS.CertFile)
	}
	if cfg.TLS.KeyFile != "" {
		cfg.TLS.KeyFile = resolvePath(base, cfg.TLS.KeyFile)
	}
	if cfg.InternalTokenFile != "" {
		cfg.InternalTokenFile = resolvePath(base, cfg.InternalTokenFile)
	}
	if cfg.OIDC.ClientSecretFile != "" {
		cfg.OIDC.ClientSecretFile = resolvePath(base, cfg.OIDC.ClientSecretFile)
	}
	return cfg, cfg.ValidateFor(mode)
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}
func localHost(host string) bool { return host == "localhost" || net.ParseIP(host).IsLoopback() }
func (c Config) Validate() error { return c.ValidateFor("serve") }
func (c Config) ValidateFor(mode string) error {
	if mode != "serve" && mode != "run" && mode != "control" && mode != "edge" && mode != "console" {
		return errors.New("unknown server mode")
	}
	if c.DevelopmentHTTP && c.TLSOffload {
		return errors.New("developmentHTTP and tlsOffload are mutually exclusive")
	}
	for _, value := range []string{c.ControlURL, c.EdgeInternalURL, c.ConsoleURL} {
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("internal URLs must be HTTP(S) origins")
		}
	}
	if mode == "edge" && (c.ControlURL == "" || c.InternalTokenFile == "") {
		return errors.New("edge requires controlURL and internalTokenFile")
	}
	if mode == "control" && (c.EdgeInternalURL == "" || c.ConsoleURL == "" || c.InternalTokenFile == "") {
		return errors.New("control requires edgeInternalURL, consoleURL and internalTokenFile")
	}
	u, err := url.Parse(c.ServerURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("serverURL must be an origin URL")
	}
	if _, err := policy.CanonicalDomain(u.Hostname()); err != nil {
		return errors.New("serverURL requires a DNS hostname; use localhost for development")
	}
	e, err := url.Parse(c.EdgeURL)
	if err != nil || e.Host == "" || e.User != nil || e.RawQuery != "" || e.Fragment != "" || e.Path != "/agent" {
		return errors.New("edgeURL must point to an origin's /agent endpoint")
	}
	host, _, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return errors.New("listenAddress must contain host and port")
	}
	if c.DevelopmentHTTP {
		if !localHost(host) || !localHost(u.Hostname()) || !localHost(e.Hostname()) || u.Scheme != "http" || e.Scheme != "http" {
			return errors.New("developmentHTTP requires loopback-only HTTP URLs and listener")
		}
	} else if u.Scheme != "https" || e.Scheme != "https" || (!c.TLSOffload && (c.TLS.CertFile == "" || c.TLS.KeyFile == "")) {
		return errors.New("HTTPS URLs and TLS cert/key or explicit tlsOffload are required")
	}
	if err := c.validateOIDC(); err != nil {
		return err
	}
	if mode == "edge" || mode == "console" {
		return nil
	}
	if c.Database.Type != "sqlite" && c.Database.Type != "postgres" {
		return errors.New("database.type must be sqlite or postgres")
	}
	if c.Database.DSN == "" {
		return errors.New("database.dsn required")
	}
	return nil
}

func (c Config) oidcRedirectURL() string {
	return strings.TrimSuffix(c.ServerURL, "/") + "/auth/oidc/callback"
}

func (c Config) validateOIDC() error {
	if !c.OIDC.Enabled {
		return nil
	}
	u, err := url.Parse(c.OIDC.Issuer)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(c.OIDC.Issuer) > 2048 || (u.Scheme != "https" && !(c.DevelopmentHTTP && u.Scheme == "http" && localHost(u.Hostname()))) {
		return errors.New("oidc.issuer must be an HTTPS issuer URL (loopback HTTP requires developmentHTTP)")
	}
	if c.OIDC.ClientID == "" || len(c.OIDC.ClientID) > 1024 || strings.ContainsAny(c.OIDC.ClientID, "\r\n\x00") {
		return errors.New("oidc.clientId is required")
	}
	if c.OIDC.RedirectURL != "" && c.OIDC.RedirectURL != c.oidcRedirectURL() {
		return errors.New("oidc.redirectURL must equal serverURL + /auth/oidc/callback")
	}
	if len(c.OIDC.Scopes) > 32 {
		return errors.New("too many OIDC scopes")
	}
	for _, scope := range c.OIDC.Scopes {
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n\x00") {
			return errors.New("invalid OIDC scope")
		}
	}
	for _, subject := range c.OIDC.BootstrapAdminSubjects {
		if subject == "" || len(subject) > 255 || strings.ContainsAny(subject, "\r\n\x00") {
			return errors.New("invalid OIDC bootstrap subject")
		}
	}
	return nil
}
func (c Config) DBOptions() db.Options {
	return db.Options{Driver: c.Database.Type, DSN: c.Database.DSN}
}
func (c Config) BaseDomain() string {
	u, _ := url.Parse(c.ServerURL)
	return strings.ToLower(u.Hostname())
}
