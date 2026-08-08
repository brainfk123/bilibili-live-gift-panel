//go:build !llamacpp

package llamacpp

import (
	"context"
	"errors"
	"testing"

	"bilibili-live-gift-panel/assistant"
)

func TestDefaultBuildReturnsUnavailableEngine(t *testing.T) {
	engine := New()
	err := engine.Load(context.Background(), "missing.gguf", assistant.LoadOptions{})
	var unavailable *assistant.EngineUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Load error = %v, want EngineUnavailableError", err)
	}
}
