package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// sendTimeout bounds `herdr-tg send`, the upload included.
const sendTimeout = 90 * time.Second

const sendUsage = `usage: herdr-tg send <image> [caption]

Posts a png, jpg, gif or webp file into the Telegram topic of the agent
running in this Herdr pane, through the running daemon. Run it from the
agent's pane, for example from a tool call, to show the operator an image
without reading it. A relative path is taken from the current directory.`

// runSend serves `herdr-tg send <image> [caption...]`.
func runSend(ctx *runContext, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(ctx.stderr, sendUsage)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	path := args[0]
	if !strings.HasPrefix(path, "~/") {
		abs, err := filepath.Abs(path)
		if err != nil {
			fmt.Fprintf(ctx.stderr, "herdr-tg send: %v\n", err)
			return exitError
		}
		path = abs
	}
	caption := strings.Join(args[1:], " ")
	c, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	sent, err := wire.sendImage(c, path, caption)
	if err != nil {
		fmt.Fprintf(ctx.stderr, "herdr-tg send: %v\n", err)
		return exitError
	}
	fmt.Fprintln(ctx.stdout, sent)
	return exitOK
}
