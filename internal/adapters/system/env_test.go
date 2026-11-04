package system_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
)

func lookupFrom(m map[string]string) system.Lookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func home() (string, error) { return "/home/u", nil }

func TestReadEnvFromFullSet(t *testing.T) {
	env, err := system.ReadEnvFrom(lookupFrom(map[string]string{
		"HERDR_PLUGIN_ID":         "x.y",
		"HERDR_PLUGIN_ROOT":       "/root",
		"HERDR_PLUGIN_CONFIG_DIR": "/cfg",
		"HERDR_PLUGIN_STATE_DIR":  "/state",
		"HERDR_SOCKET_PATH":       "/tmp/h.sock",
		"HERDR_BIN_PATH":          "/usr/local/bin/herdr",
		"HERDR_ENV":               "1",
		"LOG_LEVEL":               "debug",
		"PATH":                    "/usr/bin",
	}), home)
	if err != nil {
		t.Fatal(err)
	}
	want := system.PluginEnv{
		PluginID: "x.y", Root: "/root", ConfigDir: "/cfg", StateDir: "/state",
		SocketPath: "/tmp/h.sock", BinPath: "/usr/local/bin/herdr", LogLevel: "debug", Path: "/usr/bin", InsideHerdr: true,
	}
	if env != want {
		t.Fatalf("env = %+v\nwant %+v", env, want)
	}
}

func TestReadEnvFromFallbacks(t *testing.T) {
	env, err := system.ReadEnvFrom(lookupFrom(map[string]string{
		"HERDR_PLUGIN_CONFIG_DIR": "/cfg",
		"HERDR_PLUGIN_STATE_DIR":  "/state",
	}), home)
	if err != nil {
		t.Fatal(err)
	}
	if env.PluginID != system.DefaultPluginID || env.BinPath != "herdr" || env.InsideHerdr || env.LogLevel != "" {
		t.Fatalf("fallbacks: %+v", env)
	}
	if want := filepath.Join("/home/u", ".config", "herdr", "herdr.sock"); env.SocketPath != want {
		t.Fatalf("SocketPath = %q, want %q", env.SocketPath, want)
	}
}

func TestReadEnvFromMissingDirs(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no config dir", map[string]string{"HERDR_PLUGIN_STATE_DIR": "/s"}, "HERDR_PLUGIN_CONFIG_DIR"},
		{"no state dir", map[string]string{"HERDR_PLUGIN_CONFIG_DIR": "/c"}, "HERDR_PLUGIN_STATE_DIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := system.ReadEnvFrom(lookupFrom(tt.env), home)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want mention of %s", err, tt.want)
			}
		})
	}
}

func TestSocketPathWithoutHome(t *testing.T) {
	boom := errors.New("no home")
	_, err := system.SocketPath(lookupFrom(nil), func() (string, error) { return "", boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}

func TestReadSendTarget(t *testing.T) {
	got, err := system.ReadSendTarget(lookupFrom(map[string]string{
		"HERDR_PANE_ID":     "wA:p2",
		"HERDR_SOCKET_PATH": "/home/u/.config/herdr/sessions/agentArt/herdr.sock",
	}), home)
	want := system.SendTarget{PaneID: "wA:p2", SocketPath: "/home/u/.config/herdr/sessions/agentArt/herdr.sock",
		StateDir: filepath.Join("/home/u", ".local", "state", "herdr", "plugins", system.DefaultPluginID)}
	if err != nil || got != want {
		t.Fatalf("ReadSendTarget = %+v, %v; want %+v", got, err, want)
	}

	got, err = system.ReadSendTarget(lookupFrom(map[string]string{
		"HERDR_PANE_ID": "w1:p1", "XDG_STATE_HOME": "/xdg", "HERDR_PLUGIN_ID": "x.y",
	}), home)
	if err != nil || got.StateDir != filepath.Join("/xdg", "herdr", "plugins", "x.y") || got.SocketPath != filepath.Join("/home/u", ".config", "herdr", "herdr.sock") {
		t.Fatalf("ReadSendTarget with XDG = %+v, %v", got, err)
	}
	got, _ = system.ReadSendTarget(lookupFrom(map[string]string{"HERDR_PANE_ID": "w1:p1", "HERDR_PLUGIN_STATE_DIR": "/state"}), home)
	if got.StateDir != "/state" {
		t.Fatalf("plugin state dir ignored: %+v", got)
	}
	if _, err := system.ReadSendTarget(lookupFrom(nil), home); err == nil || !strings.Contains(err.Error(), "HERDR_PANE_ID") {
		t.Fatalf("outside a pane err = %v", err)
	}
}
