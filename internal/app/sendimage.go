package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Errors of SendImage, worded for the agent that ran `herdr-tg send`.
var (
	errSendNoImages = errors.New("image sending is not available")
	errSendNoAgent  = errors.New("no agent runs in this pane")
	errSendNoTopic  = errors.New("this agent has no open Telegram topic")
)

// SendImage posts the image file at path into the topic of the agent in
// paneID (qualified with its Herdr session), captioned with caption or the
// file's name. It is what `herdr-tg send` asks for through the control
// channel, so an agent can show the operator an image without reading it.
// The returned line says what was sent. Safe from any goroutine.
func (b *Bridge) SendImage(ctx context.Context, paneID, path, caption string) (string, error) {
	if b.images == nil {
		return "", errSendNoImages
	}
	var agent domain.Agent
	found := false
	for _, a := range b.out.live() {
		if a.Key.PaneID == paneID {
			agent, found = a, true
			break
		}
	}
	if !found {
		b.log.Info("image send refused", slog.String("pane", paneID), slog.String("reason", "no agent"))
		return "", fmt.Errorf("%w (%s)", errSendNoAgent, paneID)
	}
	entry, ok := b.out.topics.Entry(agent.Key)
	if !ok || entry.ThreadID == 0 || entry.Closed || entry.Muted {
		b.log.Info("image send refused", slog.String("key", agent.Key.String()), slog.String("reason", "no topic"))
		return "", errSendNoTopic
	}
	img, err := b.images.ReadImage(ctx, path)
	if err != nil {
		return "", err
	}
	caption = strings.TrimSpace(caption)
	if caption == "" {
		caption = "🖼 " + img.Image.Name
	}
	doc := domain.Document{ThreadID: entry.ThreadID, Name: img.Image.Name, Data: img.Image.Data, Caption: caption}
	if err := b.out.tg.SendPhoto(ctx, doc); err != nil {
		b.log.Warn("image send failed", slog.String("key", agent.Key.String()), slog.String("path", img.Path), slog.String("err", err.Error()))
		return "", fmt.Errorf("telegram: %w", err)
	}
	// The same version named in a later message is not posted again.
	b.out.linked.claim(agent.Key, img)
	b.log.Info("image sent", slog.String("key", agent.Key.String()), slog.String("path", img.Path), slog.Int("bytes", len(img.Image.Data)))
	return "sent " + img.Image.Name + " to " + agent.Label(), nil
}
