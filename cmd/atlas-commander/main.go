// Command atlas-commander is the desktop app.
//
// Start-up order matters. The gate socket is the single-instance check, so
// it comes first: a second launch asks the first to raise its window and
// exits before it touches the database or any process. Only the instance
// that won may reap processes a crashed Commander left behind, open the
// store and start the supervisor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	atlascommander "atlas-commander"
	"atlas-commander/internal/agent"
	"atlas-commander/internal/agent/chat"
	"atlas-commander/internal/agent/claudecode"
	"atlas-commander/internal/config"
	"atlas-commander/internal/demo"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/gate"
	"atlas-commander/internal/paths"
	"atlas-commander/internal/pricing"
	"atlas-commander/internal/procgroup"
	"atlas-commander/internal/store"
	"atlas-commander/internal/ui"
)

// hookTimeout bounds one approval wait inside Claude Code. A person may be
// away from the desk, so it is long; the gate itself never times out.
const hookTimeout = 24 * time.Hour

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	demoMode := flag.Bool("demo", false, "show a simulated fleet (for development and screenshots)")
	flag.Parse()
	if *showVersion {
		fmt.Println("Atlas Commander", atlascommander.Version())
		return
	}

	settings, err := config.Load(paths.Settings())
	if err != nil {
		warn(err)
	}
	if *demoMode {
		d := demo.New()
		app := ui.New(ui.Options{Controller: d, Version: atlascommander.Version(), Settings: settings})
		stop := quitOnSignal(app)
		code := app.Run()
		stop()
		d.Close() // os.Exit skips defers
		os.Exit(code)
	}
	os.Exit(run(settings))
}

func run(settings config.Settings) int {
	// The supervisor does not exist yet when the gate starts listening. No
	// agent of this instance can ask before it does, and the gate rejects
	// tokens it did not issue, so the fallback only covers that gap.
	var sup atomic.Pointer[fleet.Supervisor]
	decide := func(ctx context.Context, r gate.Request) gate.Decision {
		if s := sup.Load(); s != nil {
			return s.Gate(ctx, r)
		}
		return gate.Decision{Allow: false, Reason: "Atlas Commander is still starting. Try again in a moment."}
	}
	srv, err := gate.Listen(paths.Runtime(), decide)
	if errors.Is(err, gate.ErrRunning) {
		if gate.Activate(filepath.Join(paths.Runtime(), "gate.sock")) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "atlas-commander: another Atlas Commander is starting or not answering; try again in a moment")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "atlas-commander:", err)
		return 1
	}
	// Closed again below, before the supervisor, so no hook can ask for an
	// approval while the supervisor shuts down; Once makes the second a no-op.
	closeGate := sync.OnceFunc(func() { _ = srv.Close() })
	defer closeGate()

	instance := store.NewID()
	if n, err := procgroup.ReapOrphans(instance); err != nil {
		warn(fmt.Errorf("couldn't look for leftover agent processes: %w", err))
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "atlas-commander: stopped %d agent processes left by an earlier run\n", n)
	}

	st, err := store.Open(paths.Database())
	if err != nil {
		fmt.Fprintln(os.Stderr, "atlas-commander:", err)
		return 1
	}
	defer st.Close()

	prices, err := pricing.Load(paths.Prices())
	if err != nil {
		warn(err)
	}

	hook := hookPath()
	setup := newSetupCache(settings, hook)
	s, err := fleet.New(fleet.Options{
		Store:  st,
		Prices: prices,
		Backends: map[string]agent.Backend{
			agent.BackendClaudeCode: claudecode.New(claudecode.Options{
				Binary:      settings.ClaudePath,
				HookCommand: hook,
				HookTimeout: hookTimeout,
				NewGroup:    procgroup.New,
			}),
			agent.BackendOpenAI: chat.New(chatOptions(settings, agent.BackendOpenAI)),
			agent.BackendGemini: chat.New(chatOptions(settings, agent.BackendGemini)),
			agent.BackendLocal:  chat.New(chatOptions(settings, agent.BackendLocal)),
		},
		WorktreeRoot: paths.Worktrees(),
		Instance:     instance,
		Setup:        setup.get,
		Models: func(backend string) ([]string, error) {
			return chat.Models(context.Background(), chatOptions(settings, backend))
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "atlas-commander:", err)
		return 1
	}
	defer s.Close()
	defer closeGate() // runs before s.Close: defers are last in, first out
	s.Attach(srv)
	sup.Store(s)

	app := ui.New(ui.Options{Controller: s, Version: atlascommander.Version(), Settings: settings})
	srv.SetOnActivate(app.Raise)
	stop := quitOnSignal(app)
	defer stop()
	return app.Run()
}

// quitOnSignal makes SIGINT and SIGTERM end the UI loop, so Run returns and
// the deferred shutdown (sessions stopped, audit flushed) happens instead of
// the process dying with agents running. The returned func stops listening.
func quitOnSignal(app *ui.App) func() {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			app.Quit()
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

// hookPath is atlas-hook next to this executable, or "" when it is missing.
// Without it no tool call can be gated; the setup check says so.
func hookPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	name := "atlas-hook"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// chatOptions is the configuration of one chat provider from the settings.
// Keys are read from the environment here, once, so they are never stored.
func chatOptions(s config.Settings, backend string) chat.Options {
	switch backend {
	case agent.BackendOpenAI:
		return chat.Options{Name: backend, BaseURL: "https://api.openai.com/v1",
			APIKey: os.Getenv(s.OpenAIKeyEnv), KeyEnv: s.OpenAIKeyEnv, DefaultModel: "gpt-5"}
	case agent.BackendGemini:
		return chat.Options{Name: backend, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
			APIKey: os.Getenv(s.GeminiKeyEnv), KeyEnv: s.GeminiKeyEnv, DefaultModel: "gemini-2.5-pro"}
	}
	return chat.Options{Name: agent.BackendLocal, BaseURL: s.OllamaURL + "/v1", DefaultModel: "qwen3:8b"}
}

// setupCache answers Setup from the last check and, when that is stale,
// refreshes in the background: checking runs claude --version, which can
// take seconds, and the UI asks from the Qt thread.
type setupCache struct {
	settings config.Settings
	hook     string

	mu      sync.Mutex
	info    fleet.SetupInfo
	at      time.Time
	running bool

	// The Ollama probe is a network call with its own, shorter age.
	ollama        ollamaState
	ollamaAt      time.Time
	ollamaRunning bool
}

// ollamaState is the last answer from probing Ollama.
type ollamaState struct {
	checked, reachable bool
	models             int
}

// ollamaMaxAge keeps the Local card fresh without probing every 250 ms tick.
const ollamaMaxAge = 5 * time.Second

// setupMaxAge is how old an answer may be before a call starts a new check.
const setupMaxAge = 15 * time.Second

func newSetupCache(s config.Settings, hook string) *setupCache {
	c := &setupCache{settings: s, hook: hook}
	c.info, c.at = c.check(), time.Now()
	return c
}

func (c *setupCache) check() fleet.SetupInfo {
	d := claudecode.Detect(c.settings.ClaudePath)
	info := fleet.SetupInfo{
		ClaudePath:    d.Path,
		ClaudeVersion: d.Version,
		Problems:      d.Problems,
		OpenAIKeyEnv:  c.settings.OpenAIKeyEnv,
		OpenAIKeySet:  os.Getenv(c.settings.OpenAIKeyEnv) != "",
		GeminiKeyEnv:  c.settings.GeminiKeyEnv,
		GeminiKeySet:  os.Getenv(c.settings.GeminiKeyEnv) != "",
		OllamaURL:     c.settings.OllamaURL,
	}
	if c.hook == "" {
		info.Problems = append(info.Problems, "atlas-hook is missing from Commander's folder, so tool approvals can't work. Reinstall Atlas Commander.")
	}
	return info
}

// probeOllama asks Ollama for its models in the background; the answer lands
// in the cache for a later get.
func (c *setupCache) probeOllama() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		models, err := chat.Models(ctx, chatOptions(c.settings, agent.BackendLocal))
		c.mu.Lock()
		c.ollama = ollamaState{checked: true, reachable: err == nil, models: len(models)}
		c.ollamaAt, c.ollamaRunning = time.Now(), false
		c.mu.Unlock()
	}()
}

func (c *setupCache) get() fleet.SetupInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ollamaRunning && time.Since(c.ollamaAt) > ollamaMaxAge {
		c.ollamaRunning = true
		c.probeOllama()
	}
	if !c.running && time.Since(c.at) > setupMaxAge {
		c.running = true
		go func() {
			info := c.check()
			c.mu.Lock()
			c.info, c.at, c.running = info, time.Now(), false
			c.mu.Unlock()
		}()
	}
	info := c.info
	info.OllamaChecked, info.OllamaReachable, info.OllamaModels = c.ollama.checked, c.ollama.reachable, c.ollama.models
	return info
}

func warn(err error) { fmt.Fprintln(os.Stderr, "atlas-commander:", err) }
