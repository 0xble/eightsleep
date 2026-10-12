package ops_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0xble/toolkit"
	"github.com/0xble/toolkit/cli"
	"github.com/0xble/toolkit/op"

	"github.com/0xble/eightsleep/internal/eightfake"
	"github.com/0xble/eightsleep/ops"
)

// clock pins every test: trend samples and presence windows are relative to it.
var clock = time.Date(2026, 10, 5, 7, 30, 0, 0, time.UTC)

// TestMain sandboxes HOME and XDG so the config file and token cache are
// never the user's.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "eightsleep-ops-")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": home + "/.config", "XDG_STATE_HOME": home + "/.local/state",
		"XDG_CACHE_HOME": home + "/.cache", "XDG_DATA_HOME": home + "/.local/share", "TZ": "America/Los_Angeles"} {
		_ = os.Setenv(k, v)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

type fixture struct {
	fake *eightfake.Server
	b    *ops.Backend
	reg  *op.Registry
}

// env is the environment the backend sees: credentials only.
func env(extra map[string]string) func(string) string {
	vars := map[string]string{"EIGHTCTL_EMAIL": "ada@example.invalid", "EIGHTCTL_PASSWORD": "fixture-password", "EIGHTCTL_TIMEZONE": "America/Los_Angeles"}
	for k, v := range extra {
		vars[k] = v
	}
	return func(k string) string { return vars[k] }
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	// A later fake can reuse an earlier fake's port and thus its token-cache
	// identity. Isolate persistent credentials per fixture, not just per process.
	home := t.TempDir()
	for k, v := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": home + "/.config", "XDG_STATE_HOME": home + "/.local/state",
		"XDG_CACHE_HOME": home + "/.cache", "XDG_DATA_HOME": home + "/.local/share"} {
		t.Setenv(k, v)
	}
	fake := eightfake.New()
	t.Cleanup(fake.Close)
	fake.Now = func() time.Time { return clock }
	hosts := fake.Hosts()
	b := &ops.Backend{Globals: &ops.Globals{}, Hosts: &hosts, Getenv: env(nil), Stderr: &bytes.Buffer{},
		Now: func() time.Time { return clock }, LocalZone: func() string { return "America/Los_Angeles" }, Version: "v0.0.0-test"}
	return &fixture{fake: fake, b: b, reg: ops.New("v0.0.0-test", b)}
}

// run runs the CLI in-process and returns the exit code, stdout and stderr.
func (f *fixture) run(t testing.TB, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	o := toolkit.CLIOptions(ops.Options(f.b))
	o.Stdin, o.Stdout, o.Stderr = strings.NewReader(""), &stdout, &stderr
	code := cli.Run(context.Background(), f.reg, o, args)
	return code, stdout.String(), stderr.String()
}
