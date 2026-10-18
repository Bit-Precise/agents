package control

import "testing"

func TestOIDCConfigurationEnforcesTLSAndCallbackOrigin(t *testing.T) {
	cfg := Config{ListenAddress: ":8080", ServerURL: "https://agents.example", EdgeURL: "https://edge.agents.example/agent", TLSOffload: true, OIDC: OIDCConfig{Enabled: true, Issuer: "https://idp.example/realm", ClientID: "agents"}}
	cfg.Database.Type, cfg.Database.DSN = "sqlite", ":memory:"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, issuer := range []string{"http://idp.example", "http://127.0.0.1:8080", "https://user:secret@idp.example", "https://idp.example?query=1", "https://idp.example#fragment"} {
		copy := cfg
		copy.OIDC.Issuer = issuer
		if err := copy.Validate(); err == nil {
			t.Fatalf("unsafe issuer accepted: %s", issuer)
		}
	}
	for _, callback := range []string{"https://evil.example/auth/oidc/callback", "https://agents.example:444/auth/oidc/callback", "http://agents.example/auth/oidc/callback", "https://agents.example/auth/oidc/callback?target=evil"} {
		copy := cfg
		copy.OIDC.RedirectURL = callback
		if err := copy.Validate(); err == nil {
			t.Fatalf("unsafe callback accepted: %s", callback)
		}
	}
	cfg.OIDC.RedirectURL = cfg.ServerURL + "/auth/oidc/callback"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
