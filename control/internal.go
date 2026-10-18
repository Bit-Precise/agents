package control

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bit-precise/agents/internal/controlrpc"
)

func (a *App) internalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+controlrpc.Prefix+"identity", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.State.Identity(in.Token)
		respond(w, out, err, 200)
	})
	mux.HandleFunc("POST "+controlrpc.Prefix+"agent", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.State.Agent(in.ID)
		respond(w, out, err, 200)
	})
	mux.HandleFunc("POST "+controlrpc.Prefix+"serve", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Host, User string }
		if !decode(w, r, &in) {
			return
		}
		out, err := a.State.ResolveServe(in.Host, in.User)
		respond(w, out, err, 200)
	})
	mux.HandleFunc("POST "+controlrpc.Prefix+"egress", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Workspace, User, Host string
			Port                  int
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.State.ResolveEgress(in.Workspace, in.User, in.Host, in.Port)
		respond(w, out, err, 200)
	})
	mux.HandleFunc("POST "+controlrpc.Prefix+"watch", func(w http.ResponseWriter, r *http.Request) {
		var in controlrpc.Watch
		if !decode(w, r, &in) {
			return
		}
		events, stop := a.State.Subscribe()
		defer stop()
		if a.State.Revision() == in.Revision {
			timer := time.NewTimer(20 * time.Second)
			defer timer.Stop()
			select {
			case <-events:
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		writeJSON(w, 200, controlrpc.Watch{Revision: a.State.Revision()})
	})
	mux.HandleFunc("POST "+controlrpc.Prefix+"authorize", a.authorizeMetadata)
	return mux
}

type metadataWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *metadataWriter) Header() http.Header { return w.header }
func (w *metadataWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *metadataWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(b)
}

func (a *App) authorizeMetadata(w http.ResponseWriter, r *http.Request) {
	var in controlrpc.Metadata
	if !decode(w, r, &in) {
		return
	}
	u, err := url.ParseRequestURI(in.URI)
	controlURL, _ := url.Parse(a.Config.ServerURL)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(in.URI, "/") || strings.HasPrefix(in.URI, "//") || authorityKey(in.Host, a.publicScheme()) == "" || sameAuthority(in.Host, controlURL) {
		apiError(w, 400, "invalid", "invalid service request metadata")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), in.Method, a.publicScheme()+"://"+in.Host+in.URI, nil)
	if err != nil {
		apiError(w, 400, "invalid", "invalid service request metadata")
		return
	}
	request.Header = make(http.Header)
	for _, key := range []string{"Authorization", "Cookie", "Accept"} {
		request.Header[key] = in.Header.Values(key)
	}
	captured := &metadataWriter{header: make(http.Header)}
	var result controlrpc.MetadataResult
	if request.URL.Path == "/__agents/callback" {
		a.serviceCallback(captured, request)
	} else {
		result.Authorization, _ = a.authorizePublished(captured, request)
	}
	result.Status, result.Header, result.Body = captured.status, captured.header, captured.body.Bytes()
	writeJSON(w, 200, result)
}
