package ops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xble/toolkit"
	"github.com/0xble/toolkit/cli"

	"github.com/0xble/eightsleep/internal/client"
	"github.com/0xble/eightsleep/internal/eightfake"
	"github.com/0xble/eightsleep/internal/tokencache"
	"github.com/0xble/eightsleep/ops"
)

// cache stores a token for the fixture's identity, as a previous login would.
func cache(t *testing.T, f *fixture, email, userID string) {
	t.Helper()
	cl := client.New(email, "", "", "", "")
	cl.UseHosts(*f.b.Hosts)
	if err := tokencache.Save(cl.Identity(), eightfake.Token, time.Now().Add(time.Hour), userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tokencache.Clear(cl.Identity()) })
}

func TestCachedTokenSatisfiesAuthWithoutCredentials(t *testing.T) {
	f := newFixture(t)
	f.b.Getenv = env(map[string]string{"EIGHTCTL_PASSWORD": ""})
	cache(t, f, "ada@example.invalid", eightfake.User)
	code, out, stderr := f.run(t, "alarm", "list", "--json")
	if code != 0 || !strings.Contains(out, `"a1"`) {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	for _, r := range f.fake.Recorded() {
		if r.Host == "auth-api" || r.Path == "/v1/users/me" {
			t.Fatalf("a cached token and user need no login or lookup: %v", f.fake.Recorded())
		}
	}
}

func TestMissingCredentialsFailWithoutRequests(t *testing.T) {
	f := newFixture(t)
	f.b.Getenv = env(map[string]string{"EIGHTCTL_EMAIL": "", "EIGHTCTL_PASSWORD": ""})
	code, _, stderr := f.run(t, "--agent", "status")
	if code != 5 || !strings.Contains(stderr, `"auth_required"`) || len(f.fake.Recorded()) != 0 {
		t.Fatalf("exit %d, %d requests: %s", code, len(f.fake.Recorded()), stderr)
	}
}

func TestFixturesDoNotShareCachedCredentials(t *testing.T) {
	first := newFixture(t)
	if code, _, stderr := first.run(t, "--agent", "status"); code != 0 {
		t.Fatalf("authenticate first fixture: exit %d: %s", code, stderr)
	}
	cl := client.New("ada@example.invalid", "", "", "", "")
	cl.UseHosts(first.fake.Hosts())
	if _, err := tokencache.Load(cl.Identity()); err != nil {
		t.Fatalf("first fixture did not cache its login: %v", err)
	}

	second := newFixture(t)
	// Reuse the endpoint identity deterministically instead of waiting for the
	// OS to give a later fake the same ephemeral port during a soak run.
	second.b.Hosts = first.b.Hosts
	second.b.Getenv = env(map[string]string{"EIGHTCTL_EMAIL": "", "EIGHTCTL_PASSWORD": ""})
	before := len(first.fake.Recorded())
	code, _, stderr := second.run(t, "--agent", "status")
	requests := len(first.fake.Recorded()) - before + len(second.fake.Recorded())
	if code != 5 || !strings.Contains(stderr, `"auth_required"`) || requests != 0 {
		t.Fatalf("exit %d, %d requests: %s", code, requests, stderr)
	}
}

func TestAmbiguousCachedAccountsAreRefused(t *testing.T) {
	f := newFixture(t)
	f.b.Getenv = env(map[string]string{"EIGHTCTL_EMAIL": "", "EIGHTCTL_PASSWORD": ""})
	cache(t, f, "one@example.invalid", "u1")
	cache(t, f, "two@example.invalid", "u2")
	code, _, stderr := f.run(t, "--agent", "status")
	if code != 5 || !strings.Contains(stderr, `"ambiguous_account"`) || len(f.fake.Recorded()) != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestExplicitUserIDWinsOverCachedUser(t *testing.T) {
	f := newFixture(t)
	cache(t, f, "ada@example.invalid", "cached-user")
	code, out, _ := f.run(t, "--user-id", eightfake.User, "whoami")
	if code != 0 || out != "UserID: u1\n" {
		t.Fatalf("exit %d: %q", code, out)
	}
}

func TestInvalidHouseholdIdentityDoesNotFallBackToAuthenticatedUser(t *testing.T) {
	f := newFixture(t)
	f.fake.WrongUser = true
	code, _, stderr := f.run(t, "on")
	if code == 0 || !strings.Contains(stderr, "invalid household user response") || hasWrite(f) {
		t.Fatalf("exit %d, wrote %v: %s", code, hasWrite(f), stderr)
	}
}

func TestPresenceRejectsInvertedRange(t *testing.T) {
	f := newFixture(t)
	code, _, stderr := f.run(t, "presence", "--from", "2026-10-05", "--to", "2026-10-04")
	if code != 2 || !strings.Contains(stderr, "--to must be >= --from") || len(f.fake.Recorded()) != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestPresenceDetailWindowsFromEnvironment(t *testing.T) {
	f := newFixture(t)
	t.Setenv("EIGHTSLEEPCTL_PRESENCE_MAX_AGE_SECONDS", "60")
	code, out, stderr := f.run(t, "presence", "detail", "--json")
	if code != 0 || !strings.Contains(out, `"ambiguous_timeseries"`) || !strings.Contains(out, `"present_window_seconds": 60`) {
		t.Fatalf("a 60s window makes a 5-minute-old sample ambiguous: exit %d %s %s", code, out, stderr)
	}
}

// TestPresenceDetailKeepsAnExplicitZeroWindow: 0 is a real window, not
// "unset". A zero present window makes even a fresh sample not present, and
// a zero absent window makes every sample absent, on the flags, the
// eightsleepctl environment variables and HTTP alike.
func TestPresenceDetailKeepsAnExplicitZeroWindow(t *testing.T) {
	cases := map[string]struct {
		args []string
		env  map[string]string
	}{
		"flags": {args: []string{"presence", "detail", "--json", "--present-within", "0", "--absent-after", "0"}},
		"env": {args: []string{"presence", "detail", "--json"},
			env: map[string]string{"EIGHTSLEEPCTL_PRESENCE_MAX_AGE_SECONDS": "0", "EIGHTSLEEPCTL_ABSENCE_MIN_AGE_SECONDS": "0"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			f := newFixture(t)
			code, out, stderr := f.run(t, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			for _, want := range []string{`"present": false`, `"stale_timeseries"`, `"present_window_seconds": 0`, `"absent_window_seconds": 0`} {
				if !strings.Contains(out, want) {
					t.Fatalf("want %s in %s", want, out)
				}
			}
		})
	}
	t.Run("http", func(t *testing.T) {
		f := newFixture(t)
		status, body := httpCall(t, f, nil, "presence.detail", map[string]any{"present_window_seconds": 0, "absent_window_seconds": 0})
		if status != 200 || !strings.Contains(body, `"present_window_seconds":0`) || !strings.Contains(body, `"stale_timeseries"`) {
			t.Fatalf("status %d: %s", status, body)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		f := newFixture(t)
		code, out, stderr := f.run(t, "presence", "detail", "--json")
		if code != 0 || !strings.Contains(out, `"present_window_seconds": 1200`) || !strings.Contains(out, `"absent_window_seconds": 7200`) {
			t.Fatalf("exit %d: %s %s", code, out, stderr)
		}
	})
}

func TestSleepDayDefaultsToTodayInTheTimezone(t *testing.T) {
	f := newFixture(t)
	code, out, stderr := f.run(t, "--timezone", "Asia/Tokyo", "sleep", "day", "--json")
	if code != 0 || !strings.Contains(out, `"2026-10-05"`) {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	if q := f.fake.Recorded()[len(f.fake.Recorded())-1].Query; !strings.Contains(q, "tz=Asia/Tokyo") {
		t.Fatalf("query %q", q)
	}
}

func TestDaemonParsesTheConfigSchedule(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("timezone: UTC\nschedule:\n  - time: \"22:00\"\n    action: temp\n    temperature: \"-20\"\n  - time: \"07:00\"\n    action: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := toolkit.CLIOptions(ops.Options(f.b))
	var stdout, stderr strings.Builder
	o.Stdout, o.Stderr = &stdout, &stderr
	if code := cli.Run(ctx, f.reg, o, []string{"--config", cfg, "daemon", "--dry-run", "--pid-file", filepath.Join(dir, "d.pid")}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(f.fake.Recorded()) != 0 {
		t.Fatal("a dry-run daemon makes no request")
	}
	if err := os.WriteFile(cfg, []byte("schedule:\n  - time: \"25:00\"\n    action: \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newFixture(t)
	o = toolkit.CLIOptions(ops.Options(g.b))
	o.Stdout, o.Stderr = &stdout, &stderr
	if code := cli.Run(ctx, g.reg, o, []string{"--config", cfg, "daemon", "--dry-run"}); code == 0 {
		t.Fatal("an invalid schedule time must fail")
	}
}

func TestLogoutClearsTheCachedToken(t *testing.T) {
	f := newFixture(t)
	cache(t, f, "ada@example.invalid", eightfake.User)
	if code, out, stderr := f.run(t, "logout"); code != 0 || out != "Logged out (token cache cleared)\n" {
		t.Fatalf("exit %d: %q %s", code, out, stderr)
	}
	cl := client.New("ada@example.invalid", "", "", "", "")
	cl.UseHosts(*f.b.Hosts)
	if _, err := tokencache.Load(cl.Identity()); err == nil {
		t.Fatal("token still cached")
	}
}

func TestReadsInAFreshHomeCreateNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := newFixture(t)
	f.b.Getenv = env(map[string]string{"EIGHTCTL_EMAIL": "", "EIGHTCTL_PASSWORD": ""})
	f.run(t, "on", "--dry-run")
	f.run(t, "logout")
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("HOME changed: %v", entries)
	}
}
