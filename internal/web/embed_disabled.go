//go:build !embed_web

package web

import "net/http"

// Handler returns a placeholder page when built without -tags embed_web.
func Handler(_ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><body style="font-family:system-ui;background:#0d111a;color:#e5edf7;padding:2rem">
<h1>Asterism API Daemon Running</h1>
<p>Frontend assets are not embedded in this dev build. Run <code>cd web &amp;&amp; pnpm dev</code> and open <code>http://localhost:5173</code>, or build with <code>make build</code>.</p>
</body></html>`))
	})
}
