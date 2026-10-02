package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // registered for photoShape
	_ "image/jpeg"
	_ "image/png"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// photoMaxBytes, photoMaxSides and photoMaxRatio are Telegram's limits
	// for sendPhoto: 10 MB, width plus height at most 10000, and a side
	// ratio of at most 20.
	photoMaxBytes = 10 << 20
	photoMaxSides = 10000
	photoMaxRatio = 20
	// photoMaxEdge is the longest side Telegram keeps for a photo; a
	// longer one is scaled down to it.
	photoMaxEdge = 2560
	// photoTallRatio is the side ratio above which an image longer than
	// photoMaxEdge goes as a file: a full-page screenshot scaled down to
	// photoMaxEdge would be too narrow to read.
	photoTallRatio = 3
)

// SendPhoto posts one image as a single silent message: a compressed
// photo when its size and shape suit one, the image file otherwise. A photo
// Telegram rejects (an image it cannot process, dimensions it refuses) is
// sent once more as a file.
func (g *Gateway) SendPhoto(ctx context.Context, photo domain.Document) error {
	asPhoto, w, h := photoShape(photo.Data)
	if asPhoto {
		err := g.sendPhoto(ctx, photo)
		var api *APIError
		if err == nil {
			g.log.Debug("sendPhoto",
				slog.Int("thread_id", photo.ThreadID), slog.String("name", photo.Name), slog.Int("bytes", len(photo.Data)),
				slog.Int("width", w), slog.Int("height", h))
			return nil
		}
		if !errors.As(err, &api) || api.Code != 400 {
			g.log.Warn("sendPhoto failed",
				slog.Int("thread_id", photo.ThreadID), slog.String("name", photo.Name), slog.Int("bytes", len(photo.Data)), slog.Any("err", err))
			return fmt.Errorf("sendPhoto thread %d %q: %w", photo.ThreadID, photo.Name, err)
		}
		g.log.Warn("photo rejected, sent as a file",
			slog.Int("thread_id", photo.ThreadID), slog.String("name", photo.Name), slog.String("desc", api.Description))
	}
	if err := g.sendImageFile(ctx, photo); err != nil {
		g.log.Warn("sendDocument failed",
			slog.Int("thread_id", photo.ThreadID), slog.String("name", photo.Name), slog.Int("bytes", len(photo.Data)), slog.Any("err", err))
		return fmt.Errorf("sendDocument thread %d %q: %w", photo.ThreadID, photo.Name, err)
	}
	g.log.Debug("sendPhoto as file",
		slog.Int("thread_id", photo.ThreadID), slog.String("name", photo.Name), slog.Int("bytes", len(photo.Data)),
		slog.Int("width", w), slog.Int("height", h))
	return nil
}

func (g *Gateway) sendPhoto(ctx context.Context, photo domain.Document) error {
	params := &bot.SendPhotoParams{
		ChatID:              g.chatID,
		MessageThreadID:     photo.ThreadID,
		Caption:             photo.Caption,
		DisableNotification: true,
	}
	// A retried upload needs a fresh reader.
	return g.queue.Do(ctx, func(ctx context.Context) error {
		params.Photo = &models.InputFileUpload{Filename: photo.Name, Data: bytes.NewReader(photo.Data)}
		_, err := g.api.SendPhoto(ctx, params)
		return translate(err)
	})
}

// sendImageFile uploads the image as a file. Unlike SendDocument it lets
// Telegram detect the type, so the file shows a preview.
func (g *Gateway) sendImageFile(ctx context.Context, photo domain.Document) error {
	params := &bot.SendDocumentParams{
		ChatID:              g.chatID,
		MessageThreadID:     photo.ThreadID,
		Caption:             photo.Caption,
		DisableNotification: true,
	}
	return g.queue.Do(ctx, func(ctx context.Context) error {
		params.Document = &models.InputFileUpload{Filename: photo.Name, Data: bytes.NewReader(photo.Data)}
		_, err := g.api.SendDocument(ctx, params)
		return translate(err)
	})
}

// photoShape reports whether data suits sendPhoto, with its dimensions
// when they could be read. An image whose header Go cannot decode (WebP,
// a damaged file) goes as a file.
func photoShape(data []byte) (ok bool, w, h int) {
	if len(data) == 0 || len(data) > photoMaxBytes {
		return false, 0, 0
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return false, 0, 0
	}
	w, h = cfg.Width, cfg.Height
	long, short := max(w, h), min(w, h)
	switch {
	case w+h > photoMaxSides, long > photoMaxRatio*short:
		return false, w, h
	case long > photoMaxEdge && long > photoTallRatio*short:
		return false, w, h
	}
	return true, w, h
}
