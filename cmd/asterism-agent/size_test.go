package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// MaxBinaryBytes is the stripped agent binary budget (BLUEPRINT §16).
const MaxBinaryBytes = 11 << 20

// TestAgentBinarySize builds the release binary the way CI and packaging do
// and fails above 11 MiB. It is opt-in because it compiles per platform:
//
//	ASTERISM_AGENT_SIZE_CHECK=1 [ASTERISM_AGENT_SIZE_OUT=dir] go test -run TestAgentBinarySize ./cmd/asterism-agent
func TestAgentBinarySize(t *testing.T) {
	if os.Getenv("ASTERISM_AGENT_SIZE_CHECK") == "" {
		t.Skip("set ASTERISM_AGENT_SIZE_CHECK=1 to build and measure the agent binary")
	}
	out := os.Getenv("ASTERISM_AGENT_SIZE_OUT")
	if out == "" {
		out = t.TempDir()
	}
	for _, p := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		bin := filepath.Join(out, fmt.Sprintf("asterism-agent-%s-%s", p[0], p[1]))
		cmd := exec.Command("go", "build", "-trimpath", "-tags", "netgo,osusergo", "-ldflags", "-s -w", "-o", bin, ".")
		cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0", "GOOS="+p[0], "GOARCH="+p[1])
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s/%s: %v\n%s", p[0], p[1], err, b)
		}
		st, err := os.Stat(bin)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s/%s: %d bytes (%.2f MiB)", p[0], p[1], st.Size(), float64(st.Size())/(1<<20))
		if st.Size() > MaxBinaryBytes {
			t.Errorf("%s/%s binary is %.2f MiB, budget is 11 MiB", p[0], p[1], float64(st.Size())/(1<<20))
		}
	}
}
