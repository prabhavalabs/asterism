// Package web serves the embedded single-page app.
package web

import "bytes"

// originPlaceholder marks absolute-URL slots in web/index.html (link-preview
// tags). The handler swaps in server.public_url, or "" for relative URLs.
const originPlaceholder = "__ASTERISM_ORIGIN__"

// renderIndex fills the origin placeholder. publicURL must already be a
// validated origin (config.normalizePublicURL) or "".
func renderIndex(raw []byte, publicURL string) []byte {
	return bytes.ReplaceAll(raw, []byte(originPlaceholder), []byte(publicURL))
}
