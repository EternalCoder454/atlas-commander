// Package paths says where Commander keeps its files. Nothing else in the
// app builds a path from the home directory.
//
// The environment overrides come first so tests, benchmarks and headless
// runs can sandbox everything without touching the user's real data:
// ATLAS_CONFIG_HOME and ATLAS_DATA_HOME (the same names Atlas Notes uses),
// then the XDG variables, then the per-OS defaults from os.UserConfigDir.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// AppName is the directory name under the config and data roots on Linux.
const AppName = "atlas-commander"

// Config is the directory for settings.json, prices.json and themes/.
func Config() string {
	if d := os.Getenv("ATLAS_CONFIG_HOME"); d != "" {
		return filepath.Join(d, AppName)
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, AppName)
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil { // %APPDATA%
			return filepath.Join(d, "Atlas Commander")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, AppName)
	}
	return AppName
}

// Data is the directory for the database.
func Data() string {
	if d := os.Getenv("ATLAS_DATA_HOME"); d != "" {
		return filepath.Join(d, AppName)
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, AppName)
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Atlas Commander")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "share", AppName)
	}
	return AppName
}

// Database is the SQLite file holding fleets, agents, tasks and the audit log.
func Database() string { return filepath.Join(Data(), "commander.db") }

// Settings is the user's settings file.
func Settings() string { return filepath.Join(Config(), "settings.json") }

// Prices is the editable price table. Prices change; the app ships a default
// and writes it here on first run so the user can correct it.
func Prices() string { return filepath.Join(Config(), "prices.json") }

// Themes is the directory of user theme files (one JSON file per theme).
func Themes() string { return filepath.Join(Config(), "themes") }

// Worktrees is the root for agent git worktrees. On Windows it is kept as
// short as possible, because a repository's own deep paths plus this prefix
// can pass MAX_PATH (260) and git then fails on checkout.
func Worktrees() string {
	if d := os.Getenv("ATLAS_DATA_HOME"); d != "" {
		return filepath.Join(d, AppName, "wt")
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "AtlasCmd", "wt")
		}
	}
	return filepath.Join(Data(), "wt")
}

// Runtime is a private (0700) directory for the gate socket. It is per user
// and does not survive a reboot where the OS provides such a place.
func Runtime() string {
	if d := os.Getenv("ATLAS_RUNTIME_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, AppName)
	}
	// os.TempDir is shared on Unix, so add the uid to keep users apart; the
	// 0700 mode set by the gate server keeps them out of each other's.
	return filepath.Join(os.TempDir(), AppName+"-"+uidSuffix())
}
