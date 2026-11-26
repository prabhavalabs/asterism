package redact

import (
	"slices"
	"testing"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestRedactor(t *testing.T) {
	r, err := New(config.Redaction{ExtraSecretKeys: []string{`^internal_id$`, `(`}, DropLabels: []string{"com.example.*"}})
	if err == nil {
		t.Fatal("invalid regex not reported")
	}
	if !r.IsSecretKey("INTERNAL_ID") || !r.IsSecretKey("db_password") || r.IsSecretKey("port") {
		t.Fatal("IsSecretKey")
	}
	labels, n := r.Labels(map[string]string{"internal_id": "x", "com.example.a": "b", "keep": "v"})
	if n != 2 || len(labels) != 1 || labels["keep"] != "v" {
		t.Fatalf("%v %d", labels, n)
	}
	got := r.Args([]string{"app", "--internal_id", "42", "internal_id=7", "--token=abc", "sh -c 'x --password y'"})
	want := []string{"app", "--internal_id", "[REDACTED]", "internal_id=[REDACTED]", "--token=[REDACTED]", "sh -c 'x --password [REDACTED]'"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	c := &v1.Container{Labels: map[string]string{"com.example.x": "1"}, RedactedLabelCount: 1, Command: []string{"--internal_id", "9"}}
	r.Container(c)
	if c.RedactedLabelCount != 2 || len(c.Labels) != 0 || c.Command[1] != "[REDACTED]" {
		t.Fatalf("%v", c)
	}
	var nilR *Redactor
	nilR.Container(c)
	if l, n := nilR.Labels(map[string]string{"a": "b"}); n != 0 || len(l) != 1 {
		t.Fatal("nil redactor")
	}
}
