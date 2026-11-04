// Package system is the adapter for the process environment: the HERDR_*
// variables Herdr passes to plugin commands, process spawning and signals.
// It is one of the two packages allowed to read environment variables.
package system

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultPluginID is used when Herdr does not pass HERDR_PLUGIN_ID (older
// versions, or a developer running the binary by hand inside a pane).
const DefaultPluginID = "permgps.telegram-agents"

// PluginEnv is the plugin's view of the environment Herdr provides.
type PluginEnv struct {
	// PluginID is the manifest id, needed to open the plugin's own panes.
	PluginID string
	// Root is the plugin checkout; never used for state.
	Root string
	// ConfigDir holds config.json.
	ConfigDir string
	// StateDir holds mapping.json, the pid file and the logs.
	StateDir string
	// SocketPath is the Herdr NDJSON socket (or named pipe on Windows).
	SocketPath string
	// BinPath is the herdr CLI used for one-shot commands.
	BinPath string
	// LogLevel is the raw LOG_LEVEL value, empty when unset.
	LogLevel string
	// Path is the process PATH, forwarded to plugin panes so that the
	// manifest's relative command resolves (see herdr.CLI.OpenPane).
	Path string
	// InsideHerdr is true when HERDR_ENV=1.
	InsideHerdr bool
}

// Lookup mirrors os.LookupEnv so tests can inject an environment.
type Lookup func(key string) (string, bool)

// ReadEnv reads the real process environment.
func ReadEnv() (PluginEnv, error) {
	return ReadEnvFrom(os.LookupEnv, os.UserHomeDir)
}

// ReadEnvFrom builds a PluginEnv from the given lookup. It fails when the
// config or state directory is missing because every daemon and action
// needs both; use SocketPath for commands that only talk to the socket.
func ReadEnvFrom(lookup Lookup, home func() (string, error)) (PluginEnv, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return v
	}
	env := PluginEnv{
		PluginID:    get("HERDR_PLUGIN_ID"),
		Root:        get("HERDR_PLUGIN_ROOT"),
		ConfigDir:   get("HERDR_PLUGIN_CONFIG_DIR"),
		StateDir:    get("HERDR_PLUGIN_STATE_DIR"),
		BinPath:     get("HERDR_BIN_PATH"),
		LogLevel:    get("LOG_LEVEL"),
		Path:        get("PATH"),
		InsideHerdr: get("HERDR_ENV") == "1",
	}
	if env.PluginID == "" {
		env.PluginID = DefaultPluginID
	}
	if env.BinPath == "" {
		env.BinPath = "herdr"
	}
	if env.ConfigDir == "" {
		return env, fmt.Errorf("HERDR_PLUGIN_CONFIG_DIR is not set: run this command from Herdr")
	}
	if env.StateDir == "" {
		return env, fmt.Errorf("HERDR_PLUGIN_STATE_DIR is not set: run this command from Herdr")
	}
	sock, err := SocketPath(lookup, home)
	if err != nil {
		return env, err
	}
	env.SocketPath = sock
	return env, nil
}

// SocketPath resolves the Herdr socket the way Herdr documents it:
// HERDR_SOCKET_PATH, else ~/.config/herdr/herdr.sock.
func SocketPath(lookup Lookup, home func() (string, error)) (string, error) {
	if p, ok := lookup("HERDR_SOCKET_PATH"); ok && p != "" {
		return p, nil
	}
	dir, err := home()
	if err != nil {
		return "", fmt.Errorf("HERDR_SOCKET_PATH unset and home dir unknown: %w", err)
	}
	return filepath.Join(dir, ".config", "herdr", "herdr.sock"), nil
}

// InsideHerdr reports whether the real environment has HERDR_ENV=1.
func InsideHerdr() bool {
	return os.Getenv("HERDR_ENV") == "1"
}

// DefaultSocketPath resolves the Herdr socket from the real environment.
// It is the socket-only counterpart of ReadEnv for commands that do not
// need the plugin directories.
func DefaultSocketPath() (string, error) {
	return SocketPath(os.LookupEnv, os.UserHomeDir)
}

// SendTarget is where `herdr-tg send` delivers: the pane it runs in, the
// Herdr socket of that pane's session and the daemon's state directory.
type SendTarget struct {
	PaneID     string
	SocketPath string
	StateDir   string
}

// ReadSendTarget reads the environment of an agent's pane. Herdr passes
// HERDR_PANE_ID and HERDR_SOCKET_PATH to every pane, but the plugin's
// state directory only to plugin commands, so outside one it is the
// default, $XDG_STATE_HOME (else ~/.local/state)/herdr/plugins/<id>.
func ReadSendTarget(lookup Lookup, home func() (string, error)) (SendTarget, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return v
	}
	t := SendTarget{PaneID: get("HERDR_PANE_ID"), StateDir: get("HERDR_PLUGIN_STATE_DIR")}
	if t.PaneID == "" {
		return t, fmt.Errorf("HERDR_PANE_ID is not set: run this inside an agent's pane in Herdr")
	}
	sock, err := SocketPath(lookup, home)
	if err != nil {
		return t, err
	}
	t.SocketPath = sock
	if t.StateDir == "" {
		root := get("XDG_STATE_HOME")
		if root == "" {
			dir, err := home()
			if err != nil {
				return t, fmt.Errorf("HERDR_PLUGIN_STATE_DIR unset and home dir unknown: %w", err)
			}
			root = filepath.Join(dir, ".local", "state")
		}
		id := get("HERDR_PLUGIN_ID")
		if id == "" {
			id = DefaultPluginID
		}
		t.StateDir = filepath.Join(root, "herdr", "plugins", id)
	}
	return t, nil
}

// DefaultSendTarget is ReadSendTarget over the real environment.
func DefaultSendTarget() (SendTarget, error) {
	return ReadSendTarget(os.LookupEnv, os.UserHomeDir)
}
