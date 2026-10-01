# Atlas Commander

[![CI](https://github.com/EternalCoder454/atlas-commander/actions/workflows/ci.yml/badge.svg)](https://github.com/EternalCoder454/atlas-commander/actions/workflows/ci.yml)

Run, watch and govern a fleet of Claude agents. Atlas Commander is a desktop
application written in Go with Qt 6 Widgets: one window that shows every agent
you are running, asks you before they use a tool, tracks what they cost, and
keeps a record of everything they did. It is a sibling of
[Atlas Monitor](https://github.com/EternalCoder454/atlas-monitor) and Atlas Notes.

Linux and Windows. The design is in [`SPEC.md`](SPEC.md).

## Features

- **Board** — every agent at a glance: status, model, task, token use, cost and
  last tool call. Start, hold, resume and kill from here.
- **Approvals** — Claude Code asks before it runs a tool; the request appears
  here and you allow or deny it. See [How approvals work](#how-approvals-work).
- **Tasks** — queue work, set priorities and dependencies, and dispatch it to
  the fleet by hand.
- **Fleets** — group agents, and set cost caps per agent or per fleet.
- **Analytics** — cost, tokens and success rate per session and per agent.
- **Audit log** — an append-only record of what was asked, decided and run.
- **Observed** — a read-only view of Claude Code sessions that were started
  outside Commander.
- **Themes** — light, dark or follow the system, with a theme editor.

## Install

One line, on any Linux distribution:

```sh
curl -fsSL https://raw.githubusercontent.com/EternalCoder454/atlas-commander/release/setup.sh | bash
```

It installs what the build needs with your own package manager (it shows you the
command and asks first), builds Commander and puts it in your app menu: press
**Super** and search "Atlas". Nothing is written outside your home folder except
through your package manager. The first build compiles the Qt bindings and takes
a few minutes. `--beta` follows the beta branch, `--yes` answers yes to every
question.

**Update:** `bash ~/.local/share/atlas-commander-setup/setup.sh update`

**Uninstall:** `bash ~/.local/share/atlas-commander-setup/setup.sh uninstall`
(add `--purge` to remove settings, the database and the audit log too).

### Other ways to install

**Prebuilt tarball.** Each release carries
`atlas-commander-<version>-linux-x86_64.tar.gz`. It needs only the Qt 6 runtime
(6.5 or newer), not the `-devel` packages and no Go:

```sh
sudo dnf install qt6-qtbase qt6-qtbase-gui
tar xf atlas-commander-*-linux-x86_64.tar.gz
cd atlas-commander-* && ./install.sh      # --system for /usr/local, --uninstall to remove
```

It is built on Fedora, so it runs where Qt is at least as new. `install.sh`
checks that every library resolves before it installs anything.

**Arch Linux.** [`packaging/PKGBUILD`](packaging/PKGBUILD), then `makepkg -si`.

**RPM.** [`packaging/atlas-commander.spec`](packaging/atlas-commander.spec) for
COPR or a local `rpmbuild`.

### Windows

Download `atlas-commander-<version>-windows-x86_64.zip` from
[Releases](https://github.com/EternalCoder454/atlas-commander/releases), unpack
it anywhere and run `atlas-commander.exe`. The zip carries Qt and everything it
needs. If it does not start, run `atlas-commander-console.exe` from a terminal in
the same folder to see why. Keep `atlas-hook.exe` next to it.

## How approvals work

Commander starts Claude Code headless and registers `atlas-hook` as its
`PreToolUse` hook. Before every tool call Claude Code runs the hook, which sends
the request (tool name, input, working directory) over a local socket to
Commander and waits. Commander shows it on the Approvals page; your answer goes
back to the hook, which replies to Claude Code with allow or deny. A deny reaches
the model as an error result with your reason. If Commander is not answering, the
hook denies on its own after a timeout rather than letting the tool run.

`atlas-hook` is pure Go and has no Qt. It must sit in the same folder as
`atlas-commander`, which is where every install method above puts it. The socket
is in a private per-user directory, not on the network.

## Where your data lives

| What | Linux | Windows |
| --- | --- | --- |
| Settings, prices, themes | `~/.config/atlas-commander/` | `%APPDATA%\Atlas Commander\` |
| Database and audit log (`commander.db`) | `~/.local/share/atlas-commander/` | `%LOCALAPPDATA%\Atlas Commander\` |
| Agent git worktrees | `~/.local/share/atlas-commander/wt/` | `%LOCALAPPDATA%\AtlasCmd\wt\` |
| Gate socket | `$XDG_RUNTIME_DIR/atlas-commander/` | the temp folder, per user |

`ATLAS_CONFIG_HOME`, `ATLAS_DATA_HOME` and `ATLAS_RUNTIME_DIR` move all of it,
which is how the tests and headless runs stay away from real data. Nothing is
sent anywhere except the requests the agents themselves make to Claude.

## Build from source

You need Go 1.26 or newer, a C++ compiler, `pkg-config`, `git`, `make` and the
Qt 6.5+ development files (`setup.sh` installs these for you):

```sh
sudo dnf install golang qt6-qtbase-devel gcc-c++ pkgconf-pkg-config git make   # Fedora
sudo apt install golang-go qt6-base-dev build-essential pkg-config git make    # Debian, Ubuntu, Mint
sudo pacman -S --needed go qt6-base base-devel git pkgconf                     # Arch and family
```

```sh
make build      # bin/atlas-commander and bin/atlas-hook
make install    # into ~/.local; PREFIX=/usr for a package
```

The first build compiles the MIQT bindings and takes several minutes; later
builds are cached. On Windows, in an [MSYS2](https://www.msys2.org/) UCRT64 shell:

```sh
pacman -S --needed mingw-w64-ucrt-x86_64-{go,gcc,pkgconf,qt6-base} git zip
bash packaging/stage-windows.sh
```

That produces `dist/atlas-commander-<version>-windows-x86_64.zip`, the same way CI
does: both call the same script.

## Development

- `make vet` and `make test` mirror CI. `make test` points the config, data and
  runtime directories at a temp folder and unsets the display, so GUI tests skip.
- Windows compile check for the packages with no Qt:
  `GOOS=windows go vet ./internal/agent/... ./internal/procgroup/... ./internal/gate/... ./internal/store/... ./cmd/atlas-hook`
- CI ([`ci.yml`](.github/workflows/ci.yml)) runs gofmt, vet, build and tests on
  Fedora, and the race detector over the packages that share state;
  [`windows.yml`](.github/workflows/windows.yml) builds the Windows zip.

## Versioning and releases

[Semantic Versioning](https://semver.org). The version is in [`VERSION`](VERSION).
Day-to-day work happens on `beta` (versioned `X.Y.Z-beta`); a release is merged
into `release`, the `-beta` suffix dropped, and tagged `vX.Y.Z`. Pushing the tag
builds the Linux tarball and the Windows zip, each with a `.sha256`, and attaches
them to that tag's GitHub Release. [`CHANGELOG.md`](CHANGELOG.md) lists what
changed.

## Project layout

```
cmd/atlas-commander/   the Qt application
cmd/atlas-hook/        the PreToolUse hook helper (no Qt)
internal/agent/        backend contract; Claude Code and Claude API backends
internal/fleet/        supervisor: backends, gate, store, cost caps
internal/gate/         local socket the hook talks to
internal/store/        SQLite store and the append-only audit log
internal/ui/           everything that imports Qt
internal/procgroup/    process groups (Linux) and Job Objects (Windows)
internal/paths/        where every file lives
packaging/             RPM spec, PKGBUILD, tarball installer, Windows staging
```

## Licence

MIT, see [`LICENSE`](LICENSE). Third-party notices are in [`NOTICE`](NOTICE).
