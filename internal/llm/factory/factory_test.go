package factory

import (
	"io"
	"log/slog"
	"testing"

	"qq-bot/internal/config"
)

func TestNewOpenAIChat(t *testing.T) {
	client, err := New(config.LLMConfig{
		Protocol: "openai-chat", BaseURL: "http://localhost/v1", APIKey: "test", Timeout: "2s",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if client.Name() != "openai-chat" {
		t.Fatalf("client = %s", client.Name())
	}
}
