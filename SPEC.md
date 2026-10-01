# Atlas Commander

## What it is

A **desktop GUI** for observing, managing, and orchestrating a **fleet of Claude agents** (Claude Code, OpenAI, Gemini or a local model). Think mission control: you're the **Commander**, and your agents are your crew.

## Target user

Solo developers and small teams running multiple Claude agents in parallel (coding, research, ops) who want a single pane of glass instead of juggling terminal tabs.

## Platform

- **Stack:** Go with Qt 6 Widgets, using MIQT bindings (MIT licensed, CGo)
- **Minimum Qt:** 6.5 (needed for the system color scheme API; verify MIQT binds it)
- **v1 targets:** Linux and Windows, same as Atlas Notes and Atlas Monitor
- **Local-first:** single user, no cloud dependency
- **Shared code:** one codebase, with platform-specific code isolated behind small interfaces (process control, paths)

## Core capabilities

| Layer | What the Commander does |
|-------|------------------------|
| **Observe** | Live status board: agent name, model, task, token burn, latency, last tool call, git worktree, error state |
| **Manage** | Start / hold / resume / kill individual agents; re-prompt or redirect mid-task; global kill-all with confirm step |
| **Orchestrate** | Assign tasks to the fleet, set priority, define dependency chains (manual dispatch only) |
| **Govern** | Approval gates (human-in-the-loop), cost caps per agent/fleet, append-only audit log |
| **Analyze** | Per-session and per-agent cost breakdown, success rate, token efficiency (queries on the audit log) |

## Design language

Draws from the **Atlas** family aesthetic:

- **From Atlas Notes:** clean, minimal chrome; a polished theme editor with light/dark/system-follow modes; user-customizable accent colors, fonts, and density (compact vs. comfortable); subtle borders and small radii rather than heavy shadows.
- **From Atlas Monitor:** compact data-dense rows (not big cards); color-coded status dots (green = healthy, amber = warning, red = error); monospace for all numeric/telemetry data; thin grid lines; charts with bordered plots on a light grid, reading value above, axis labels below; a single accent bar to mark the active view (Task Manager pattern).

**Key visual principles:**
- Neutral base (dark or light), one accent color per state
- JetBrains Mono (or equivalent) for all data readouts, bundled in the app resources so it looks the same on Linux and Windows
- 44px row height (40px in compact density)
- No glow, no blur on data elements: flat, legible, fast
- Collapsible sidebar
- Native title bar in v1. A unified title bar and sidebar frame needs a custom frameless window, so it is a stretch goal.

## Architecture

### Agent backend interface
- Internal Go interface: Start, Stop, Send, Event stream
- v1 backends: Claude Code, and chat providers (OpenAI, Gemini, Local through Ollama)
- Other model providers can be added later without UI changes

### Agent control
- Commander launches agents as child processes and owns their input, output, and exit state
- Claude Code runs in headless mode with streaming JSON, giving structured events for tokens, tool calls, and errors
- Approval gates use Claude Code pre-tool-use hooks
- The hook runs a small helper executable (shipped with Commander) that sends the request to Commander and waits for the decision. No shell scripts, so it behaves the same on Linux and Windows.
- Verify exact flags and hook behavior against current Claude Code docs before building, on both platforms
- **Observed mode:** read-only view of sessions started outside Commander (no process sniffing)

### Pause and kill behavior
- "Pause" means hold at the next tool call. Do not freeze the process (SIGSTOP does not exist on Windows, and it can break the API connection on Linux).
- **Linux:** agents run in process groups so a Commander crash leaves no orphans
- **Windows:** agents are assigned to a Job Object with kill-on-close, which gives the same guarantee
- Both live behind one Go interface (`ProcessGroup`) with build tags per OS

### Local communication
- Hook helper to Commander uses a local-only channel with a per-session token
- QLocalServer gives a Unix domain socket on Linux and a named pipe on Windows with one API
- Alternative: Go's own socket support plus a named pipe library on Windows

### Cost caps
- Computed from usage numbers in the event stream
- Price table lives in a config file, since prices change

### Isolation
- Each coding agent gets its own git worktree to prevent file conflicts
- Each agent has its own working directory
- On Windows, watch for long path limits inside worktrees. Keep the worktree root short.

### Persistence
- Fleet and agent state in SQLite (modernc.org/sqlite, pure Go)
- A restart must not lose the board
- Audit log is append-only, one row per event
- Data and config live in the standard per-OS locations (`os.UserConfigDir` / `os.UserCacheDir`), never hardcoded paths

### Notifications
- Desktop notifications for approval requests and errors
- Use QSystemTrayIcon with its message API on both platforms
- **Inference:** confirm it shows proper notifications on common Linux desktops and Windows 10/11. Test early.

### Theme system
- Theme files as JSON, previewable without applying (same pattern as the Atlas Notes theme editor)
- Use the Fusion style as the base so widgets look the same on both platforms
- Apply themes with a QPalette plus a generated Qt style sheet (QSS)
- Follow-system mode reads the OS color scheme from QStyleHints
- Atlas Notes and Atlas Monitor may still use GTK. The theme JSON format can be shared; the code that applies it cannot.

### Multi-fleet
- A fleet is a named group with a working directory, a budget, and a list of agents
- Commander can manage multiple fleets from day one

## Packaging and build

- MIQT needs a working Qt 6 C++ toolchain (headers, pkg-config, a C++ compiler) on every build machine
- The first build takes about 10 minutes. Cache the Go build cache in CI.
- **Licensing:** ship Qt as dynamic LGPL libraries. Static linking needs a commercial Qt license. Include the LGPL notices and keep the Qt libraries replaceable.
- Windows: installer bundles the Qt DLLs and plugins (use `windeployqt`)
- Linux: depend on system Qt 6, or ship an AppImage
- Build with `-ldflags "-s -w"`
- Claude Code must be installed on the machine; Commander detects it and shows a clear setup message if it is missing
- **Inference:** Claude Code on Windows may need Git for Windows. Confirm in the docs and add it to the setup check.

## Qt implementation notes

- Qt UI objects must only be touched from the main thread. MIQT locks the main goroutine to the Qt thread when the app is created. Agent goroutines post updates to it (for example a queued signal). Verify the exact MIQT call.
- Batch UI updates at about 4 per second to avoid flicker
- Status board uses `QTableView` with a custom `QAbstractTableModel` and a custom delegate for status dots and monospace cells. Qt only paints visible rows, so 50+ agents stay fast.
- **Inference:** custom models and delegates need virtual method overrides. Confirm MIQT supports them cleanly with a small spike.
- Charts are drawn with `QPainter` in a custom widget. Qt Charts is skipped because of its license and possible lack of bindings.
- Bundle JetBrains Mono with `QFontDatabase` and embed it with `miqt-rcc` resources
- Use `miqt-uic` only if you want Qt Designer files; hand-written layouts are fine for v1

## Agent registration form

- Name
- Backend (Claude Code, OpenAI, Gemini or Local)
- Model
- Fleet
- Working directory
- Pinned prompt (sent at the start of every session)
- Cost cap

## Differentiators vs. existing tools

| Gap in the market | Atlas Commander's answer |
|---|---|
| Most tools are coding-agent-specific | Framework-agnostic; works with any Claude-powered agent |
| Dev-tool feel (terminal-heavy, git-centric) | Clean mission-control GUI, no terminal required |
| Enterprise tools are overkill | Local-first, single-user, no SSO or policy engine |
| No consistent "Atlas" design identity | Cohesive visual language shared across the Atlas app family |

## Non-goals (v1)

- No cloud sync or multi-user
- No non-Claude model support (the backend interface leaves room for later)
- No automated scheduling / cron (manual dispatch only)
- No macOS build (Qt makes it easier later)
- No public plugin API
- No local HTTP/WS API for outside tools (planned for v2)
- No custom frameless title bar (stretch goal)
- No QML; v1 uses Qt Widgets only

## Decisions

- **Toolkit:** Qt 6 Widgets through MIQT
- **Agent discovery:** explicit launch is the main path; observed mode is read-only
- **Multi-fleet:** supported in v1
- **Plugins:** deferred to v2; the backend interface stays internal until then
- **Platforms:** Linux and Windows in v1, with OS-specific code kept behind interfaces
