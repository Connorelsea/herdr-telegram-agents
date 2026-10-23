package testkit

import (
	"context"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeTranscriber is a scripted domain.Transcriber: Transcribe records the
// path and answers the text and error set for it.
type FakeTranscriber struct {
	mu    sync.Mutex
	text  string
	err   error
	calls []string
}

var _ domain.Transcriber = (*FakeTranscriber)(nil)

// NewFakeTranscriber returns a fake answering text for every file.
func NewFakeTranscriber(text string) *FakeTranscriber {
	return &FakeTranscriber{text: text}
}

// Set scripts the next answers.
func (f *FakeTranscriber) Set(text string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.text, f.err = text, err
}

// Calls returns the paths Transcribe was asked for, in order.
func (f *FakeTranscriber) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Transcribe implements domain.Transcriber.
func (f *FakeTranscriber) Transcribe(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
	return f.text, f.err
}
