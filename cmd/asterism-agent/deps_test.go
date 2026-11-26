package main

import (
	"os/exec"
	"strings"
	"testing"
)

// deniedDeps must never be linked into the agent binary (BLUEPRINT D21):
// they belong to the hub or would blow the 11 MiB / 15 MB budgets.
var deniedDeps = []string{
	"github.com/shirou/gopsutil",
	"github.com/moby/",
	"github.com/docker/docker",
	"modernc.org/sqlite",
	"github.com/spf13/cobra",
	"golang.org/x/net",
	"github.com/prabhavalabs/asterism/internal/service",
	"github.com/prabhavalabs/asterism/internal/store",
	"github.com/prabhavalabs/asterism/internal/api",
	"github.com/prabhavalabs/asterism/internal/mcp",
	"github.com/prabhavalabs/asterism/internal/web",
	"github.com/prabhavalabs/asterism/internal/auth",
}

func TestAgentDeps(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	for _, goos := range []string{"linux", "darwin"} {
		cmd := exec.Command("go", "list", "-deps", ".")
		cmd.Env = append(cmd.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps (%s): %v\n%s", goos, err, out)
		}
		for _, pkg := range strings.Fields(string(out)) {
			for _, d := range deniedDeps {
				if pkg == strings.TrimSuffix(d, "/") || strings.HasPrefix(pkg, d) {
					t.Errorf("%s: asterism-agent links denied package %s (rule %q)", goos, pkg, d)
				}
			}
		}
	}
}
