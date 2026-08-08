//go:build windows && cgo && llamacpp

package llamacpp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/assistant"
)

func TestRealQwenSmoke(t *testing.T) {
	modelPath := os.Getenv("ASSISTANT_TEST_MODEL")
	if modelPath == "" {
		t.Skip("set ASSISTANT_TEST_MODEL to a verified Qwen3-0.6B Q8_0 GGUF for the release smoke test")
	}
	engine := New()
	defer engine.Unload()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	loadStarted := time.Now()
	if err := engine.Load(ctx, modelPath, assistant.LoadOptions{ContextSize: 4096, Threads: 4}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loadDuration := time.Since(loadStarted)
	if loadDuration > 15*time.Second {
		t.Fatalf("cold load took %v, want <= 15s", loadDuration)
	}
	var answer strings.Builder
	generatedPieces := 0
	var firstTokenAt time.Time
	generationStarted := time.Now()
	err := engine.Generate(ctx, assistant.GenerateRequest{
		Prompt:    "<|im_start|>system\n你是测试助手。只回答：连接直播间。<|im_end|>\n<|im_start|>user\n怎么开始？<|im_end|>\n<|im_start|>assistant\n",
		MaxTokens: 384, Temperature: 0.7, TopP: 0.8, TopK: 20,
	}, func(token string) error {
		if firstTokenAt.IsZero() {
			firstTokenAt = time.Now()
		}
		generatedPieces++
		answer.WriteString(token)
		return nil
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	rawAnswer := answer.String()
	if strings.TrimSpace(rawAnswer) == "" {
		t.Fatal("Generate returned an empty answer")
	}
	thinkStart := strings.Index(strings.ToLower(rawAnswer), "<think>")
	if thinkStart < 0 {
		t.Fatalf("thinking mode did not emit a <think> block: %q", rawAnswer)
	}
	thinkEnd := strings.Index(strings.ToLower(rawAnswer[thinkStart:]), "</think>")
	if thinkEnd < 0 {
		t.Fatalf("unclosed thinking markup: %q", rawAnswer)
	}
	thinkEnd += thinkStart
	if hidden := rawAnswer[thinkStart+len("<think>") : thinkEnd]; strings.TrimSpace(hidden) == "" {
		t.Fatalf("thinking mode emitted an empty reasoning block: %q", rawAnswer)
	}
	visibleAnswer := rawAnswer[:thinkStart] + rawAnswer[thinkEnd+len("</think>"):]
	if strings.TrimSpace(visibleAnswer) == "" {
		t.Fatalf("Generate returned no visible answer: %q", rawAnswer)
	}
	if firstTokenAt.IsZero() {
		t.Fatal("generation did not emit a token")
	}
	firstTokenLatency := firstTokenAt.Sub(generationStarted)
	if firstTokenLatency > 5*time.Second {
		t.Fatalf("first token latency = %v, want <= 5s", firstTokenLatency)
	}
	if generatedPieces >= 5 {
		generationDuration := time.Since(firstTokenAt)
		piecesPerSecond := float64(generatedPieces-1) / generationDuration.Seconds()
		if piecesPerSecond < 5 {
			t.Fatalf("generation speed = %.2f token pieces/s, want >= 5", piecesPerSecond)
		}
	}
	t.Logf("cold load=%v, first token=%v, generated pieces=%d", loadDuration, firstTokenLatency, generatedPieces)
}
