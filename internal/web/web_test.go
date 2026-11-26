package web

import "testing"

func TestRenderIndex(t *testing.T) {
	raw := []byte(`<meta property="og:image" content="__ASTERISM_ORIGIN__/og-image.png"><meta property="og:url" content="__ASTERISM_ORIGIN__/">`)
	tests := []struct{ origin, want string }{
		{"https://monitor.example.com", `<meta property="og:image" content="https://monitor.example.com/og-image.png"><meta property="og:url" content="https://monitor.example.com/">`},
		{"", `<meta property="og:image" content="/og-image.png"><meta property="og:url" content="/">`},
	}
	for _, tt := range tests {
		if got := string(renderIndex(raw, tt.origin)); got != tt.want {
			t.Fatalf("renderIndex(%q) = %s", tt.origin, got)
		}
	}
}
