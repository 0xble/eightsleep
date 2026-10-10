# eightsleep

`eightsleep` controls an Eight Sleep Pod from a terminal, a script or an
agent: temperature, power, alarms, away mode and audio, plus sleep and
presence data. It was `eightctl`, a fork of
[steipete/eightctl](https://github.com/steipete/eightctl) by Peter Steinberger,
and releases still ship the same binary under that name.

> [!IMPORTANT]
> Eight Sleep publishes no stable public API. `eightsleep` uses the company's
> cloud endpoints, so provider changes and rate limits can interrupt commands.
> There is no local or Bluetooth control.

Every command is an operation declared once on
[toolkit](https://github.com/0xble/toolkit), so the same operation is a CLI
command, an HTTP route (`eightsleep serve --socket PATH`), an MCP tool
(`eightsleep mcp`) and an entry in `eightsleep metadata --json`.

## Install

```sh
./bin/setup          # builds into ~/.local/bin, with eightctl linked to it
eightsleep --version
```

Releases carry `eightsleep` and `eightctl` for macOS, Linux and Windows on
amd64 and arm64.

## Quick Start

```sh
export EIGHTCTL_EMAIL="you@example.com"
export EIGHTCTL_PASSWORD="your-password"

eightsleep status
eightsleep temp 20
eightsleep temp -40 --side right
eightsleep off
```

`status`, `on`, `off` and `temp` act on every discovered household side unless
you select one with `--side left|right|solo` or `--target-user-id <id>`.

## Commands

| Area | Commands |
| --- | --- |
| Pod control | `status`, `on`, `off`, `temp`, `away on/off/status` |
| Account | `whoami`, `logout`, `version` |
| Sleep data | `sleep day/range`, `presence`, `presence detail`, `metrics`, `schedule list` |
| Alarms | `alarm list/active/create/update/delete/snooze/dismiss/dismiss-all/vibration-test` |
| Pod features | `audio`, `base`, `device`, `tempmode`, `autopilot`, `travel`, `household` |
| Automation | `daemon` (runs the config file's `schedule:`) |
| Surfaces | `serve`, `mcp`, `metadata` |

Run `eightsleep --help` or `eightsleep <command> --help` for flags.

Writes apply immediately on the command line, as `eightctl` did, and `--dry-run`
previews them without changing the bed. Over HTTP and MCP a write needs
`"apply": true`, and `alarm delete` and `travel delete-trip` also
`"confirm": true`. `serve` refuses applied writes by default.

## Configuration

Settings come from flags, then `EIGHTSLEEP_*` or `EIGHTCTL_*` environment
variables, then the YAML config file: `~/.config/eightsleep/config.yaml` when
it exists, else `~/.config/eightctl/config.yaml`, or `--config`.

```yaml
email: "you@example.com"
password: "your-password"
timezone: "America/New_York"   # or "local"
# client_id / client_secret default to the public app client
```

Keep the file mode `600`; a readable file prints a warning. Cached tokens live
in a file keyring under `~/.config/eightctl/keyring`, so headless runs never
prompt. `logout` clears the cache without revoking the token.

The daemon reads a `schedule:` list from the same file:

```yaml
schedule:
  - time: "22:00"
    action: "temp"
    temperature: "-20"
  - time: "07:00"
    action: "off"
```

## Output

Row commands print a table, or `--output json|csv`. `--json` (or `--agent`)
prints the operation's result as JSON on every command, `--fields a,b` filters
that JSON, and failures print a JSON error envelope on stderr. Exit codes follow
the family table: 0 success, 1 error, 2 usage, 3 not found, 5 auth, 6 rate
limited, 7 timeout.

## Development

Releases are cut from the fleet control plane, not from a workflow in this
repository: tag the release commit, then run `tools/bin/release eightsleep`
from dotfiles. It runs this repository's `.goreleaser.yml`, publishes the
GitHub release with both binaries and verifies the archives against
`checksums.txt`. `goreleaser build --snapshot --clean` builds the same
archives locally without publishing.

```sh
./bin/ci preflight   # quick local checks
./bin/check          # format, vet, lint, race tests, build, scripts
```

`docs/compatibility.md` records the `eightctl` and `eightsleepctl` behaviour
this rewrite keeps, and `internal/compat` replays it against goldens recorded
from both old programs on a fake Eight Sleep API. Tests never reach the real
service.

## License

[MIT](LICENSE), copyright (c) 2025 Peter Steinberger, the upstream author.
