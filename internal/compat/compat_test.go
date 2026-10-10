// Package compat_test replays every eightctl command shape and every
// eightsleepctl command against the fake Eight Sleep and compares the result
// with golden output recorded from the pre-toolkit programs: eightctl at
// origin/main a2b8291 (0.2.8-0xble.0.1.0) and the eightsleepctl script.
//
// For each case it compares the exit code, the exact provider requests
// (method, host, path, sorted query), the stdout JSON or text, and the error
// message. The fake's address and the sandbox path are normalised. Dates and
// timestamps are masked only in cases marked clock, whose program derives
// them from the wall clock; every other case is compared verbatim whatever
// the run date.
//
// Re-record with internal/compat/record.sh.
package compat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/0xble/toolkit"
	"github.com/0xble/toolkit/cli"

	"github.com/0xble/eightsleep/internal/eightfake"
	"github.com/0xble/eightsleep/ops"
)

var (
	recordEightctl      = flag.String("record-eightctl", "", "path to the pre-toolkit eightctl; rewrite its goldens")
	recordEightsleepctl = flag.String("record-eightsleepctl", "", "path to the eightsleepctl script; rewrite its goldens")
	recordPython        = flag.String("record-python", "python3", "Python 3.10 or later to run the eightsleepctl script with")
)

func TestMain(m *testing.M) {
	// Every program runs in UTC, so default dates agree between the old
	// programs and the replay whatever the machine's zone.
	time.Local = time.UTC
	os.Exit(m.Run())
}

const (
	eightctl      = "eightctl"
	eightsleepctl = "eightsleepctl"
)

type tcase struct {
	name string
	// caller names who runs this invocation; see docs/compatibility.md.
	caller string
	// old is the program the golden was recorded from.
	old  string
	args []string
	// newArgs, when set, is the eightsleep invocation: the equivalent of an
	// eightsleepctl command, or a new spelling of an eightctl one. Otherwise
	// eightsleep runs args.
	newArgs []string
	fake    func(*eightfake.Server)
	// noConfig runs without a config file; userID puts user_id in it.
	noConfig bool
	userID   bool
	// change, when set, is a documented intentional difference in the
	// output: stdout and the error message are compared with the documented
	// replacement below instead of the golden. Exit code, requests and write
	// bodies are still compared unless exit, requests or refused say
	// otherwise.
	change string
	// changed replaces the listed top-level keys of the golden's stdout JSON
	// object; every other key still compares with the golden.
	changed map[string]any
	// newJSONFrom names another golden whose stdout JSON is the replacement.
	newJSONFrom string
	// newJSON, newText and newError are the replacement stdout and error.
	newJSON  any
	newText  string
	newError string
	// exit is the new exit code when it moved to the family table (C1).
	exit int
	// requests documents why the provider request list differs, and skips
	// only that list: the bodies of the applied writes are still compared.
	requests string
	// refused means the new program rejects the invocation before any
	// request (C12): it must make no request and apply no write.
	refused bool
	// clock marks a case whose program derives dates or timestamps from the
	// wall clock: today's and yesterday's UTC dates, and timestamps on them,
	// are masked. Without it no date is masked, so a fixture date that happens
	// to be today still compares literally.
	clock bool
}

var (
	failGet = func(host, path string, status int) func(*eightfake.Server) {
		return func(s *eightfake.Server) { s.Fail = map[string]int{"GET " + host + " " + path: status} }
	}
	solo = func(s *eightfake.Server) { s.Solo = true }
)

var cases = []tcase{
	// Reads, in each output format.
	{name: "status", args: []string{"status"}},
	{name: "status-json", args: []string{"status", "--output", "json"}},
	{name: "status-side", args: []string{"status", "--side", "left", "--output", "json"}},
	{name: "status-all-csv", args: []string{"--output", "csv", "status", "--all-sides"}},
	{name: "status-unknown-user", args: []string{"status", "--target-user-id", "u9", "--output", "json"}},
	{name: "status-solo", args: []string{"status"}, fake: solo},
	{name: "status-no-household", args: []string{"status", "--output", "json"}, fake: failGet("client-api", "/v1/users/u2", 500)},
	{name: "whoami", args: []string{"whoami"}},
	{name: "whoami-offline", args: []string{"--user-id", "u1", "whoami"}, noConfig: true},
	{name: "version", args: []string{"version"}, change: "the version is the release tag", newText: "dev\n"},
	{name: "version-flag", args: []string{"--version"}, change: "the version is the release tag", newText: "dev\n"},
	{name: "tracks", args: []string{"tracks"}},
	{name: "tracks-json", args: []string{"tracks", "--output", "json"}},
	{name: "feats-csv", args: []string{"feats", "--output", "csv"}},
	{name: "alarm-list", args: []string{"alarm", "list"}, change: "a set sound prints its ID; eightctl printed a pointer address",
		newText: "id  time   enabled  days         vibration       sound\na1  07:00  true     [1 2 3 4 5]  {true rise 50}  chime\na2  09:30  false    [0 6]        {false  0}      <nil>\n"},
	{name: "alarm-list-json", args: []string{"alarm", "list", "--output", "json"}},
	{name: "alarm-list-fallback", args: []string{"alarm", "list", "--output", "json"}, fake: failGet("client-api", "/v1/users/u1/alarms", 404)},
	{name: "alarm-list-fields", args: []string{"--fields", "id,time", "alarm", "list", "--output", "json"},
		change: "--fields filters --json output only (C4)", newJSONFrom: "alarm-list-json"},
	{name: "presence-window", args: []string{"presence", "--from", "2026-10-04", "--to", "2026-10-05", "--output", "json"}},
	// eightctl had no "presence check"; it is the new explicit spelling of
	// presence (C7), recorded from eightctl's presence.
	{name: "presence-check", args: []string{"presence", "--from", "2026-10-04", "--to", "2026-10-05", "--output", "json"},
		newArgs: []string{"presence", "check", "--from", "2026-10-04", "--to", "2026-10-05", "--output", "json"}},
	{name: "presence-default", args: []string{"presence"}, clock: true},
	{name: "schedule", args: []string{"schedule", "list", "--output", "json"}},
	{name: "schedule-none", args: []string{"schedule", "list"}, fake: func(s *eightfake.Server) { s.NoSchedule = true }},
	{name: "sleep-day", args: []string{"sleep", "day", "--date", "2026-10-04"}},
	{name: "sleep-day-json", args: []string{"sleep", "day", "--date", "2026-10-04", "--output", "json"}},
	{name: "sleep-day-today", args: []string{"sleep", "day", "--output", "json"}, clock: true},
	{name: "sleep-range", args: []string{"sleep", "range", "--from", "2026-10-03", "--to", "2026-10-04", "--output", "json"}},
	{name: "nap-status", args: []string{"tempmode", "nap", "status", "--output", "json"}},
	{name: "hotflash-status", args: []string{"tempmode", "hotflash", "status"}},
	{name: "temp-events", args: []string{"tempmode", "events", "--from", "2026-10-01", "--to", "2026-10-02", "--output", "json"}},
	{name: "audio-tracks", args: []string{"audio", "tracks"}},
	{name: "audio-categories", args: []string{"audio", "categories", "--output", "json"}},
	{name: "audio-state", args: []string{"audio", "state", "--output", "json"}},
	{name: "audio-next", args: []string{"audio", "next", "--output", "json"}},
	{name: "audio-favorites", args: []string{"audio", "favorites", "list", "--output", "json"}},
	{name: "base-info", args: []string{"base", "info", "--output", "json"}},
	{name: "base-presets", args: []string{"base", "presets"}},
	{name: "device-info", args: []string{"device", "info", "--output", "json"}},
	{name: "device-peripherals", args: []string{"device", "peripherals", "--output", "json"}},
	{name: "device-owner", args: []string{"device", "owner", "--output", "json"}},
	{name: "device-owner-fallback", args: []string{"device", "owner", "--output", "json"}, fake: func(s *eightfake.Server) {
		s.Fail = map[string]int{"GET client-api /v1/devices/d1/owner": 404, "GET app-api /v1/devices/d1/owner": 404}
	}},
	{name: "device-warranty", args: []string{"device", "warranty", "--output", "json"}},
	{name: "device-online", args: []string{"device", "online", "--output", "json"}},
	{name: "device-priming-tasks", args: []string{"device", "priming-tasks", "--output", "json"}},
	{name: "device-priming-schedule", args: []string{"device", "priming-schedule", "--output", "json"}},
	{name: "metrics-trends", args: []string{"metrics", "trends", "--from", "2026-10-01", "--to", "2026-10-02", "--output", "json"}, clock: true},
	{name: "metrics-intervals", args: []string{"metrics", "intervals", "--id", "s1", "--output", "json"}},
	{name: "metrics-summary", args: []string{"metrics", "summary", "--output", "json"}},
	{name: "metrics-aggregate", args: []string{"metrics", "aggregate", "--output", "json"}},
	{name: "metrics-insights", args: []string{"metrics", "insights", "--output", "json"}},
	{name: "autopilot-details", args: []string{"autopilot", "details", "--output", "json"}},
	{name: "autopilot-history", args: []string{"autopilot", "history", "--output", "json"}},
	{name: "autopilot-recap", args: []string{"autopilot", "recap", "--output", "json"}},
	{name: "travel-trips", args: []string{"travel", "trips", "--output", "json"}},
	{name: "travel-plans", args: []string{"travel", "plans", "--trip", "trip1", "--output", "json"}},
	{name: "travel-tasks", args: []string{"travel", "tasks", "--plan", "p1", "--output", "json"}},
	{name: "travel-airports", args: []string{"travel", "airport-search", "--query", "SFO", "--output", "json"}},
	{name: "travel-flight", args: []string{"travel", "flight-status", "--flight", "UA1", "--output", "json"}},
	{name: "household-summary", args: []string{"household", "summary", "--output", "json"}},
	{name: "household-schedule", args: []string{"household", "schedule", "--output", "json"}},
	{name: "household-current-set", args: []string{"household", "current-set", "--output", "json"}},
	{name: "household-invitations", args: []string{"household", "invitations", "--output", "json"}},
	{name: "household-devices", args: []string{"household", "devices", "--output", "json"}},
	{name: "household-users", args: []string{"household", "users", "--output", "json"}},
	{name: "household-guests", args: []string{"household", "guests", "--output", "json"}},
	{name: "household-guests-missing", args: []string{"household", "guests", "--output", "json"},
		fake: failGet("app-api", "/v1/household/users/u1/guests", 404)},
	{name: "away-status", args: []string{"away", "status"}},
	{name: "away-status-side", args: []string{"away", "status", "--side", "right", "--output", "json"}},

	// Writes.
	{name: "on", args: []string{"on"}},
	{name: "off-side", args: []string{"off", "--side", "left"}},
	{name: "on-user", args: []string{"on", "--target-user-id", "u2"}},
	{name: "temp", args: []string{"temp", "20"}},
	{name: "temp-fahrenheit-user", args: []string{"temp", "68F", "--target-user-id", "u2"}},
	{name: "temp-negative-dashdash", args: []string{"temp", "--side", "right", "--", "-40"}},
	{name: "temp-negative", args: []string{"temp", "-40", "--side", "right"}},
	{name: "alarm-create", args: []string{"alarm", "create", "--time", "07:30", "--days", "1,2,3"}},
	{name: "alarm-create-full", args: []string{"alarm", "create", "--time", "06:00", "--days", "0", "--disabled", "--no-vibration", "--sound", "chime"}},
	{name: "alarm-update", args: []string{"alarm", "update", "a1", "--enabled=false", "--time", "06:45"}},
	{name: "alarm-delete", args: []string{"alarm", "delete", "a2"}},
	{name: "alarm-snooze", args: []string{"alarm", "snooze", "a1"}},
	{name: "alarm-dismiss", args: []string{"alarm", "dismiss", "a1"}},
	{name: "alarm-dismiss-all", args: []string{"alarm", "dismiss-all"}, requests: "C11: PUT on the app API instead of POST on the client API"},
	{name: "alarm-vibration-test", args: []string{"alarm", "vibration-test"}},
	{name: "away-on", args: []string{"away", "on"}, clock: true},
	{name: "away-off-both", args: []string{"away", "off", "--both"}, clock: true},
	{name: "away-on-side", args: []string{"away", "on", "--side", "left"}, clock: true},
	{name: "away-on-quiet", args: []string{"--quiet", "away", "on"}, clock: true},
	{name: "nap-on", args: []string{"tempmode", "nap", "on"}},
	{name: "nap-off", args: []string{"tempmode", "nap", "off"}},
	{name: "nap-extend", args: []string{"tempmode", "nap", "extend"}},
	{name: "hotflash-on", args: []string{"tempmode", "hotflash", "on"}},
	{name: "hotflash-off", args: []string{"tempmode", "hotflash", "off"}},
	{name: "audio-play", args: []string{"audio", "play", "--track", "t1"}},
	{name: "audio-pause", args: []string{"audio", "pause"}},
	{name: "audio-seek", args: []string{"audio", "seek", "--position", "5000"}},
	{name: "audio-volume-zero", args: []string{"audio", "volume", "--level", "0"}},
	{name: "audio-pair", args: []string{"audio", "pair"}},
	{name: "audio-favorite-add", args: []string{"audio", "favorites", "add", "--track", "t1"}},
	{name: "audio-favorite-remove", args: []string{"audio", "favorites", "remove", "--track", "t1"}},
	{name: "base-angle", args: []string{"base", "angle", "--head", "10", "--foot", "5"}},
	{name: "base-preset-run", args: []string{"base", "preset-run", "--name", "flat"}},
	{name: "base-test", args: []string{"base", "test"}},
	{name: "level-suggestions-off", args: []string{"autopilot", "level-suggestions", "--enabled=false"}},
	{name: "snore-mitigation", args: []string{"autopilot", "snore-mitigation"}},
	{name: "travel-create-trip", args: []string{"travel", "create-trip", "--destination", "Tokyo", "--trip-timezone", "Asia/Tokyo"}},
	{name: "travel-delete-trip", args: []string{"travel", "delete-trip", "--trip", "trip1"}},
	{name: "travel-create-plan", args: []string{"travel", "create-plan", "--trip", "trip1", "--name", "Adjust"}},
	{name: "travel-update-plan", args: []string{"travel", "update-plan", "--plan", "p1", "--date", "2026-10-09"}},
	{name: "logout", args: []string{"logout"}},

	// Error paths.
	{name: "err-no-credentials", args: []string{"status"}, noConfig: true, exit: 5},
	{name: "err-config-missing", args: []string{"--config", "/nonexistent/eightsleep.yaml", "status"}, noConfig: true},
	{name: "err-token-refused", args: []string{"alarm", "list"}, exit: 5, fake: func(s *eightfake.Server) {
		s.Fail = map[string]int{"POST auth-api /v1/tokens": 401}
	}},
	{name: "err-provider-500", args: []string{"status", "--side", "left"}, fake: failGet("app-api", "/v1/users/u1/temperature", 500)},
	{name: "err-not-found", args: []string{"metrics", "intervals", "--id", "s1"}, exit: 3, fake: func(s *eightfake.Server) {
		s.Fail = map[string]int{"GET client-api /v1/users/u1/intervals/s1": 404, "GET app-api /v1/users/u1/intervals/s1": 404}
	}},
	{name: "err-side-unknown", args: []string{"status", "--side", "middle"}, exit: 2},
	{name: "err-side-and-user", args: []string{"on", "--side", "left", "--target-user-id", "u2"}, exit: 2},
	{name: "err-all-sides-conflict", args: []string{"status", "--all-sides", "--side", "left"}, exit: 2},
	{name: "err-both-conflict", args: []string{"away", "on", "--both", "--side", "left"}, exit: 2},
	{name: "err-temp-invalid", args: []string{"temp", "warm"}, exit: 2},
	{name: "err-temp-missing", args: []string{"temp"}, exit: 2, change: "a parse error prints one error line (C2)", newError: `expected "<value>"`},
	{name: "err-alarm-create", args: []string{"alarm", "create"}, exit: 2, refused: true},
	{name: "err-alarm-update-empty", args: []string{"alarm", "update", "a1"}, exit: 2, refused: true},
	{name: "err-favorite-track", args: []string{"audio", "favorites", "add"}, exit: 2, refused: true},
	{name: "err-create-trip-empty", args: []string{"travel", "create-trip"}, exit: 2, refused: true},
	{name: "err-sleep-range-missing", args: []string{"sleep", "range"}, exit: 2, refused: true},
	{name: "err-presence-date", args: []string{"presence", "--from", "2026-13-01"}, exit: 2, refused: true},
	{name: "err-daemon-no-schedule", args: []string{"daemon", "--dry-run"}, exit: 2},
	{name: "err-unknown-command", args: []string{"definitely-not-a-command"}, exit: 2, change: "a parse error prints one error line (C2)",
		newError: "unexpected argument definitely-not-a-command"},
	{name: "err-unknown-flag", args: []string{"status", "--definitely-not-a-flag"}, exit: 2, change: "a parse error prints one error line (C2)",
		newError: "unknown flag --definitely-not-a-flag"},
	{name: "err-bare", args: []string{}, exit: 2, change: "a bare invocation is a usage error instead of help (C1)",
		newError: `expected one of "alarm", "audio", "autopilot", "away", "base", ...`},

	// eightsleepctl, run as the script with --output json and as the
	// eightsleep equivalent with --json (docs/compatibility.md#eightsleepctl).
	{name: "esc-whoami", old: eightsleepctl, userID: true, args: []string{"whoami"}, newArgs: []string{"whoami", "--json"},
		change: "token_expires_at is present only when a token is cached", requests: "eightsleep answers a configured user ID without requesting a token",
		newJSON: map[string]any{"user_id": "u1"}},
	{name: "esc-alarm-list", old: eightsleepctl, userID: true, args: []string{"alarm", "list"}, newArgs: []string{"alarm", "list", "--json"},
		change: "rows {id,time,enabled,days,vibration,sound} instead of the raw alarm objects", requests: "eightctl's alarm list reads the client API, the script the app API",
		newJSONFrom: "alarm-list-json"},
	{name: "esc-alarm-active", old: eightsleepctl, userID: true, args: []string{"alarm", "active"}, newArgs: []string{"alarm", "active", "--json"}},
	{name: "esc-dismiss-all", old: eightsleepctl, userID: true, args: []string{"alarm", "dismiss-all"}, newArgs: []string{"alarm", "dismiss-all", "--json"}},
	{name: "esc-dismiss-all-fallback", old: eightsleepctl, userID: true, args: []string{"alarm", "dismiss-all"}, newArgs: []string{"alarm", "dismiss-all", "--json"},
		fake: func(s *eightfake.Server) { s.DismissAllStatus = 405 }},
	{name: "esc-dismiss-all-dry-run", old: eightsleepctl, userID: true, args: []string{"alarm", "dismiss-all", "--dry-run"},
		newArgs: []string{"alarm", "dismiss-all", "--dry-run", "--json"},
		change:  "fallback names the route the fallback really uses (POST each active alarm's dismiss) instead of a routines PUT",
		changed: map[string]any{"fallback": "<fake>/app-api/v1/users/u1/alarms/{id}/dismiss (POST for each active alarm)"}},
	{name: "esc-presence", old: eightsleepctl, userID: true, args: []string{"presence"}, newArgs: []string{"presence", "detail", "--json"}, clock: true},
	{name: "esc-presence-stale", old: eightsleepctl, userID: true, args: []string{"presence"}, newArgs: []string{"presence", "detail", "--json"}, clock: true,
		fake: func(s *eightfake.Server) { s.StaleSignals = true }},
}

// golden is what one case produced.
type golden struct {
	Caller     string              `json:"caller,omitempty"`
	Program    string              `json:"program"`
	Args       []string            `json:"args"`
	NewArgs    []string            `json:"new_args,omitempty"`
	Exit       int                 `json:"exit"`
	StdoutJSON any                 `json:"stdout_json,omitempty"`
	StdoutText string              `json:"stdout_text,omitempty"`
	Error      string              `json:"error,omitempty"`
	Requests   []eightfake.Request `json:"requests"`
	// Writes are the bodies of the applied changes.
	Writes []eightfake.Write `json:"writes,omitempty"`
	Change string            `json:"change,omitempty"`
}

func TestCallers(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.name] {
			t.Fatalf("duplicate case %s", c.name)
		}
		seen[c.name] = true
		if c.old == "" {
			c.old = eightctl
		}
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join("testdata", c.name+".json")
			bin := map[string]string{eightctl: *recordEightctl, eightsleepctl: *recordEightsleepctl}[c.old]
			if *recordEightctl != "" || *recordEightsleepctl != "" {
				if bin == "" {
					t.Skipf("no %s to record from", c.old)
				}
				g := runOld(t, bin, c)
				b, _ := json.MarshalIndent(g, "", "  ")
				if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no golden for %s; record it with internal/compat/record.sh: %v", c.name, err)
			}
			var want golden
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			for _, d := range diff(c, want, runNew(t, c)) {
				t.Error(d)
			}
		})
	}
}

// loadGolden reads a case's golden.
func loadGolden(t *testing.T, name string) golden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("no golden for %s; record it with internal/compat/record.sh: %v", name, err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// diff lists how got differs from the golden want.
func diff(c tcase, want, got golden) []string {
	var out []string
	wantExit := want.Exit
	if c.exit != 0 {
		wantExit = c.exit
	}
	if got.Exit != wantExit {
		out = append(out, fmt.Sprintf("exit %d, want %d (old program %d)", got.Exit, wantExit, want.Exit))
	}
	switch {
	case c.refused:
		if len(got.Requests) != 0 || len(got.Writes) != 0 {
			out = append(out, fmt.Sprintf("refused invocation made %d requests and %d writes", len(got.Requests), len(got.Writes)))
		}
	case c.requests != "":
		// The route differs as documented; the bodies sent must not.
		if g, w := writeBodies(got.Writes), writeBodies(want.Writes); !reflect.DeepEqual(g, w) {
			out = append(out, fmt.Sprintf("write bodies differ:\n new %s\n old %s", mustJSON(g), mustJSON(w)))
		}
	default:
		if !reflect.DeepEqual(got.Requests, want.Requests) {
			out = append(out, fmt.Sprintf("provider requests differ:\n new %v\n old %v", got.Requests, want.Requests))
		}
		if !reflect.DeepEqual(roundTrip(got.Writes), roundTrip(want.Writes)) {
			out = append(out, fmt.Sprintf("write bodies differ:\n new %s\n old %s", mustJSON(got.Writes), mustJSON(want.Writes)))
		}
	}
	if c.change != "" {
		want = replacement(c, want)
		if got.Error != want.Error {
			out = append(out, fmt.Sprintf("error differs from the documented change:\n new %q\n want %q", got.Error, want.Error))
		}
	}
	if !reflect.DeepEqual(roundTrip(got.StdoutJSON), roundTrip(want.StdoutJSON)) {
		g, _ := json.Marshal(got.StdoutJSON)
		w, _ := json.Marshal(want.StdoutJSON)
		out = append(out, fmt.Sprintf("stdout JSON differs:\n new %s\n old %s", g, w))
	}
	if got.StdoutText != want.StdoutText {
		out = append(out, fmt.Sprintf("stdout text differs:\n new %q\n old %q", got.StdoutText, want.StdoutText))
	}
	if c.change == "" && want.Error != "" && got.Error != want.Error {
		out = append(out, fmt.Sprintf("error differs:\n new %q\n old %q", got.Error, want.Error))
	}
	return out
}

// replacement is the output a change case documents in place of the
// golden's: the golden with the changed keys replaced, another golden's
// stdout, or the literal stdout and error.
func replacement(c tcase, want golden) golden {
	out := golden{StdoutText: c.newText, Error: c.newError}
	switch {
	case c.changed != nil:
		obj, _ := roundTrip(want.StdoutJSON).(map[string]any)
		if obj == nil {
			obj = map[string]any{}
		}
		for k, v := range c.changed {
			obj[k] = v
		}
		out.StdoutJSON = obj
	case c.newJSONFrom != "":
		b, err := os.ReadFile(filepath.Join("testdata", c.newJSONFrom+".json"))
		if err != nil {
			panic(err)
		}
		var g golden
		if err := json.Unmarshal(b, &g); err != nil {
			panic(err)
		}
		out.StdoutJSON = g.StdoutJSON
	case c.newJSON != nil:
		out.StdoutJSON = c.newJSON
	}
	return out
}

func writeBodies(ws []eightfake.Write) []any {
	out := []any{}
	for _, w := range ws {
		out = append(out, roundTrip(w.Body))
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestChangesDocumentTheirReplacement keeps every change case asserting its
// new output rather than skipping the comparison.
func TestChangesDocumentTheirReplacement(t *testing.T) {
	for _, c := range cases {
		has := c.changed != nil || c.newJSONFrom != "" || c.newJSON != nil || c.newText != "" || c.newError != ""
		if c.change != "" && !has {
			t.Errorf("%s: change %q names no replacement output", c.name, c.change)
		}
		if c.change == "" && has {
			t.Errorf("%s: a replacement output without a documented change", c.name)
		}
		if c.refused && c.requests != "" {
			t.Errorf("%s: refused already skips the request list", c.name)
		}
	}
}

func roundTrip(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// sandbox is the private home every run gets: a config file with fixture
// credentials (unless the case has none), UTC, and the fake's address.
func sandbox(t *testing.T, c tcase, fake *eightfake.Server) (map[string]string, string) {
	t.Helper()
	home := t.TempDir()
	if !c.noConfig {
		dir := filepath.Join(home, ".config", "eightctl")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := "email: ada@example.invalid\npassword: fixture-password\ntimezone: UTC\nclient_id: fixture-client\nclient_secret: fixture-secret\n"
		if c.userID {
			cfg += "user_id: u1\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"TZ": "UTC", "USER": "compat", "EIGHTSLEEP_COMPAT_BASE": fake.URL,
	}, home
}

func newFake(c tcase) *eightfake.Server {
	fake := eightfake.New()
	if c.fake != nil {
		c.fake(fake)
	}
	return fake
}

func runOld(t *testing.T, bin string, c tcase) golden {
	fake := newFake(c)
	defer fake.Close()
	env, home := sandbox(t, c, fake)
	args := c.args
	cmd := exec.Command(bin, args...)
	if c.old == eightsleepctl {
		args = append([]string{"--config", filepath.Join(home, ".config", "eightctl", "config.yaml"), "--output", "json"}, c.args...)
		cmd = exec.Command(*recordPython, append([]string{"-I", bin}, args...)...)
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, strings.NewReader("")
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	errLine := ""
	if m := regexp.MustCompile(`(?m)^Error: (.*)$`).FindStringSubmatch(stderr.String()); m != nil {
		errLine = m[1]
	}
	g := result(c, time.Now(), code, stdout.String(), errLine, fake, home)
	// Every eightsleepctl case is a success that reaches the provider. A
	// nonzero exit or no request means the script did not run as recorded
	// (an old Python, a missing module), not a contract to keep.
	if c.old == eightsleepctl && (g.Exit != 0 || len(g.Requests) == 0) {
		t.Fatalf("eightsleepctl recorded exit %d with %d requests; stderr:\n%s", g.Exit, len(g.Requests), stderr.String())
	}
	return g
}

func runNew(t *testing.T, c tcase) golden {
	return runNewAt(t, c, time.Now())
}

// runNewAt runs the new CLI as if the run date were now's: the fake and the
// backend both read now as the clock, and the result is normalised against
// it. One instant serves all three, so a run across midnight cannot split
// them.
func runNewAt(t *testing.T, c tcase, now time.Time) golden {
	fake := newFake(c)
	defer fake.Close()
	fake.Now = func() time.Time { return now }
	env, home := sandbox(t, c, fake)
	for k, v := range env {
		t.Setenv(k, v)
	}
	// Unset every variable the new program reads, so the caller's
	// environment cannot change a case. Set-then-unset restores it afterwards.
	scrub := []string{"EIGHTSLEEPCTL_PRESENCE_MAX_AGE_SECONDS", "EIGHTSLEEPCTL_ABSENCE_MIN_AGE_SECONDS"}
	for _, p := range []string{"EIGHTCTL_", "EIGHTSLEEP_"} {
		for _, k := range []string{"EMAIL", "PASSWORD", "USER_ID", "CLIENT_ID", "CLIENT_SECRET", "TIMEZONE", "OUTPUT", "CONFIG", "QUIET", "CONFIG_QUIET", "VERBOSE"} {
			scrub = append(scrub, p+k)
		}
	}
	for _, k := range scrub {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	hosts := fake.Hosts()
	b := &ops.Backend{Globals: &ops.Globals{}, Hosts: &hosts, Stderr: &bytes.Buffer{}, Version: "dev",
		Now: func() time.Time { return now }}
	args := c.args
	if c.newArgs != nil {
		args = c.newArgs
	}
	var stdout, stderr bytes.Buffer
	o := toolkit.CLIOptions(ops.Options(b))
	o.Stdin, o.Stdout, o.Stderr = strings.NewReader(""), &stdout, &stderr
	code := cli.Run(context.Background(), ops.New("dev", b), o, args)
	errLine := ""
	if m := regexp.MustCompile(`(?m)^error: (.*)$`).FindStringSubmatch(stderr.String()); m != nil {
		errLine = m[1]
	}
	if c.newArgs == nil && stdout.Len() == 0 && stderr.Len() > 0 && strings.HasPrefix(stderr.String(), "{") {
		var env struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(stderr.Bytes(), &env) == nil {
			errLine = env.Error.Message
		}
	}
	return result(c, now, code, stdout.String(), errLine, fake, home)
}

var (
	rfc3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)
	port    = regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
	// recent is a timestamp the fake derived from the wall clock.
	recent = regexp.MustCompile(`<(today|yesterday)>T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)
)

func result(c tcase, now time.Time, code int, stdout, errLine string, fake *eightfake.Server, home string) golden {
	reqs := fake.Recorded()
	if reqs == nil {
		reqs = []eightfake.Request{}
	}
	clean := cleaner(c, now, home)
	for i := range reqs {
		reqs[i].Query = clean(reqs[i].Query)
	}
	writes, _ := fake.Snapshot().([]eightfake.Write)
	if len(writes) == 0 {
		writes = nil
	}
	for i := range writes {
		writes[i].Body = normalise(writes[i].Body, clean)
	}
	g := golden{Caller: c.caller, Program: c.old, Args: c.args, NewArgs: c.newArgs, Exit: code, Requests: reqs,
		Writes: writes, Change: c.change, Error: clean(errLine)}
	if g.Program == "" {
		g.Program = eightctl
	}
	var v any
	if err := json.Unmarshal([]byte(stdout), &v); err == nil && strings.TrimSpace(stdout) != "" {
		g.StdoutJSON = normalise(v, clean)
	} else {
		g.StdoutText = clean(stdout)
	}
	return g
}

// cleaner returns the string normaliser for one run of c at now: the sandbox
// path and the fake's address always, today's and yesterday's UTC dates and
// timestamps on them only when c is a clock case. A clock case also masks
// timestamps on the real wall clock's last three UTC days: away on and off
// stamp their write from time.Now, which a pinned now does not reach.
func cleaner(c tcase, now time.Time, home string) func(string) string {
	today := now.UTC().Format("2006-01-02")
	yesterday := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	wall := time.Now().UTC()
	var days []string
	for i := 0; i < 3; i++ {
		days = append(days, wall.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	wallRecent := regexp.MustCompile(`(` + strings.Join(days, "|") + `)T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)
	return func(s string) string {
		s = strings.ReplaceAll(s, home, "<home>")
		s = port.ReplaceAllString(s, "<fake>")
		if !c.clock {
			return s
		}
		s = strings.ReplaceAll(s, today, "<today>")
		s = strings.ReplaceAll(s, yesterday, "<yesterday>")
		s = recent.ReplaceAllString(s, "<recent>")
		return wallRecent.ReplaceAllString(s, "<recent>")
	}
}

// normalise masks values that depend on the wall clock: timestamps the fake
// derives from now, token expiries and signal ages.
func normalise(v any, clean func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch {
			case k == "age_seconds" && val != nil:
				x[k] = "<age>"
			case (k == "timestamp" || k == "token_expires_at") && isTime(val):
				x[k] = "<time>"
			default:
				x[k] = normalise(val, clean)
			}
		}
	case []any:
		for i := range x {
			x[i] = normalise(x[i], clean)
		}
	case string:
		return clean(x)
	}
	return v
}

func isTime(v any) bool {
	s, ok := v.(string)
	return ok && rfc3339.MatchString(s)
}

// TestEveryCaseHasAGolden keeps the table and testdata in step.
func TestEveryCaseHasAGolden(t *testing.T) {
	if *recordEightctl != "" || *recordEightsleepctl != "" {
		t.Skip("recording")
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	if len(files) != len(cases) {
		t.Errorf("%d goldens for %d cases", len(files), len(cases))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		found := false
		for _, c := range cases {
			found = found || c.name == name
		}
		if !found {
			t.Errorf("golden %s has no case", name)
		}
	}
}

var (
	dateLiteral = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	// masked is a mask in a golden, which encoding/json writes as \u003c...\u003e.
	masked = regexp.MustCompile(`(<|\\u003c)(today|yesterday|recent)(>|\\u003e)`)
)

// TestClockMaskingMatchesGoldens keeps clock in step with the goldens: a
// golden holds a masked date exactly when its case is a clock case.
func TestClockMaskingMatchesGoldens(t *testing.T) {
	for _, c := range cases {
		b, err := os.ReadFile(filepath.Join("testdata", c.name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if has := masked.Match(b); has != c.clock {
			t.Errorf("%s: golden has masked dates %v, case clock %v", c.name, has, c.clock)
		}
	}
}

// TestGoldensHoldOnEveryRunDate replays every non-clock case whose golden
// holds a literal date as if the run date were that date and the next day,
// the two days a today-or-yesterday mask would rewrite it. travel-update-plan
// (--date 2026-10-09) is the case that failed on those dates when every case
// was masked.
func TestGoldensHoldOnEveryRunDate(t *testing.T) {
	if *recordEightctl != "" || *recordEightsleepctl != "" {
		t.Skip("recording")
	}
	covered := map[string]bool{}
	for _, c := range cases {
		if c.clock {
			continue
		}
		if c.old == "" {
			c.old = eightctl
		}
		b, err := os.ReadFile(filepath.Join("testdata", c.name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		dates := map[string]bool{}
		for _, d := range dateLiteral.FindAllString(string(b), -1) {
			dates[d] = true
		}
		for d := range dates {
			day, err := time.Parse("2006-01-02", d)
			if err != nil {
				continue
			}
			want := loadGolden(t, c.name)
			for _, now := range []time.Time{day.Add(12 * time.Hour), day.Add(36 * time.Hour)} {
				t.Run(c.name+"@"+now.Format("2006-01-02"), func(t *testing.T) {
					for _, d := range diff(c, want, runNewAt(t, c, now)) {
						t.Error(d)
					}
				})
			}
			covered[c.name] = true
		}
	}
	if !covered["travel-update-plan"] {
		t.Fatal("travel-update-plan was not replayed on its fixture date")
	}
}

// TestClockCasesHoldOnEveryRunDate replays every clock case as if the run
// date were several days of the month. sleep-day-today failed on every day
// but the 8th when the fake derived today's score and duration from the run
// date's day of month.
func TestClockCasesHoldOnEveryRunDate(t *testing.T) {
	if *recordEightctl != "" || *recordEightsleepctl != "" {
		t.Skip("recording")
	}
	runDates := []time.Time{
		time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 0, 30, 0, 0, time.UTC),
		time.Date(2027, 1, 8, 12, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 9, 12, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 31, 23, 30, 0, 0, time.UTC),
		time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC),
	}
	n := 0
	for _, c := range cases {
		if !c.clock {
			continue
		}
		if c.old == "" {
			c.old = eightctl
		}
		want := loadGolden(t, c.name)
		b, _ := json.Marshal(want)
		for _, now := range runDates {
			// A run date on or the day after a fixture date would mask it;
			// the dates above must stay clear of every golden's literals.
			for _, d := range dateLiteral.FindAllString(string(b), -1) {
				for _, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
					if day.UTC().Format("2006-01-02") == d {
						t.Fatalf("%s: run date %s would mask fixture date %s; pick another", c.name, now.Format(time.RFC3339), d)
					}
				}
			}
			t.Run(c.name+"@"+now.Format("2006-01-02"), func(t *testing.T) {
				for _, d := range diff(c, want, runNewAt(t, c, now)) {
					t.Error(d)
				}
			})
			n++
		}
	}
	if n == 0 {
		t.Fatal("no clock case was replayed")
	}
}

// TestDateMaskWouldBreakTravelUpdatePlan proves the replay above is
// sensitive: masking travel-update-plan as a clock case on its fixture date
// and the day after rewrites the fixture date and the golden no longer
// matches.
func TestDateMaskWouldBreakTravelUpdatePlan(t *testing.T) {
	if *recordEightctl != "" || *recordEightsleepctl != "" {
		t.Skip("recording")
	}
	var c tcase
	for _, k := range cases {
		if k.name == "travel-update-plan" {
			c = k
		}
	}
	c.old, c.clock = eightctl, true
	want := loadGolden(t, c.name)
	for _, now := range []time.Time{time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)} {
		if len(diff(c, want, runNewAt(t, c, now))) == 0 {
			t.Errorf("masking dates on %s left the golden matching; the replay would not catch the bug", now.Format("2006-01-02"))
		}
	}
}
