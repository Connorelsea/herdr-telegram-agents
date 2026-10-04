// Package compose wires adapters into the ports the application and the
// CLI depend on. It is the only package that knows every concrete adapter,
// so the CLI can stay free of adapter imports.
package compose

import (
	"context"
	"log/slog"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/herdr"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// HerdrGateway is the Herdr port together with its lifecycle.
type HerdrGateway interface {
	domain.HerdrGateway
	// Start connects to the socket and launches the event stream.
	Start(ctx context.Context) error
	// Close stops the stream and drops the connections.
	Close() error
	// AgentSession returns the transient session tuple Herdr reports for a
	// pane, for a reply source that needs the value for one lookup.
	AgentSession(ctx context.Context, paneID string) (domain.SessionTuple, error)
}

// NewHerdrGateway builds the Herdr adapter with the daemon's default
// reconnect schedule. It follows every session of the Herdr config root
// that holds the socket at path (the default server and each `herdr
// --session NAME`), so one daemon serves all of them.
func NewHerdrGateway(path string, log *slog.Logger) HerdrGateway {
	return herdr.NewMultiGateway(herdr.SessionSockets(path), log, herdr.DefaultBackoff)
}
