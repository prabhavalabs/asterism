package mcp

import "github.com/prabhavalabs/asterism/internal/apiclient"

// NewRemoteBackend returns a Backend that calls a running hub over ConnectRPC,
// authenticating with an API bearer token (read scope).
func NewRemoteBackend(baseURL, token string) Backend {
	return apiclient.New(baseURL, token)
}
