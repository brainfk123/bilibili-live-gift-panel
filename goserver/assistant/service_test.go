package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeEngine struct {
	mu                          sync.Mutex
	loads, unloads, generations int
	prompt                      string
	loadErr                     error
	generate                    func(context.Context, TokenCallback) error
}

func (engine *fakeEngine) Load(context.Context, string, LoadOptions) error {
	engine.mu.Lock()
	engine.loads++
	err := engine.loadErr
	engine.mu.Unlock()
	return err
}
func (engine *fakeEngine) Generate(ctx context.Context, request GenerateRequest, callback TokenCallback) error {
	engine.mu.Lock()
	engine.generations++
	engine.prompt = request.Prompt
	generate := engine.generate
	engine.mu.Unlock()
	if generate != nil {
		return generate(ctx, callback)
	}
	return callback("<think>不应泄露</think>请先填写房间号。")
}
func (engine *fakeEngine) Unload() error {
	engine.mu.Lock()
	engine.unloads++
	engine.mu.Unlock()
	return nil
}

func serviceWithActiveModel(t *testing.T, engine Engine, idle time.Duration) *Service {
	t.Helper()
	data := fakeGGUF(t)
	sum := sha256.Sum256(data)
	manifest := ModelManifest{SchemaVersion: 1, ModelID: "test", Version: "v1", Repository: "Qwen/test", Revision: "abc", File: "model.gguf", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Architecture: "qwen3", Quantization: "Q8_0"}
	root := t.TempDir()
	path := filepath.Join(root, "models", manifest.Version, "model.gguf")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewModelStore(ModelStoreOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(manifest, path); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{Knowledge: EmbeddedKnowledge(), Store: store, Engine: engine, AppVersion: "0.2.4", IdleTimeout: idle, State: func(context.Context) (StateSummary, error) {
		return StateSummary{AppVersion: "0.2.4", Connection: "connected", Login: "logged_in", RoomConfigured: true, AttributeCount: 2, RuleCount: 3, TimerCount: 1}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestChatUsesEvidenceAndStripsThinking(t *testing.T) {
	engine := &fakeEngine{}
	service := serviceWithActiveModel(t, engine, time.Hour)
	events := []StreamEvent{}
	err := service.Chat(context.Background(), ChatRequest{Question: "房间号在哪里看"}, func(event StreamEvent) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	var text string
	if len(events) < 3 || events[0].Type != "sources" || len(events[0].Sources) == 0 {
		t.Fatalf("events = %#v", events)
	}
	for _, event := range events {
		if event.Type == "delta" {
			text += event.Text
		}
	}
	if strings.Contains(text, "泄露") || text != "请先填写房间号。" {
		t.Fatalf("answer = %q", text)
	}
	if !strings.Contains(engine.prompt, "<|im_start|>system") || strings.Contains(engine.prompt, "/no_think") || strings.Contains(engine.prompt, "真实房间号") {
		t.Fatalf("prompt = %q", engine.prompt)
	}
	if !strings.Contains(engine.prompt, "帮助条目足够回答") || strings.Contains(engine.prompt, NoEvidenceAnswer) {
		t.Fatalf("prompt must tell the small model to answer the server-approved evidence directly: %q", engine.prompt)
	}
	if !strings.Contains(engine.prompt, "第一句直接回答问题") || strings.Contains(engine.prompt, "加粗结论") {
		t.Fatalf("prompt must request answer content without a repeatable formatting label: %q", engine.prompt)
	}
	last := events[len(events)-1]
	if last.Type != "done" || last.StateSummary == nil || last.StateSummary.RoomConfigured != true {
		t.Fatalf("done = %#v", last)
	}
}

func TestPromptIgnoresConversationHistoryAndKeepsCurrentQuestionLast(t *testing.T) {
	knowledge, err := NewKnowledgeBase(EmbeddedKnowledge())
	if err != nil {
		t.Fatal(err)
	}
	results := knowledge.Search("怎么设置定时器", maximumSources)
	prompt, err := buildPrompt(ChatRequest{
		Question: "怎么设置定时器？",
		History: []ChatMessage{
			{Role: "user", Content: "怎么连接直播间？"},
			{Role: "assistant", Content: "先填写房间号并连接。"},
		},
	}, results, StateSummary{Connection: "connected", AttributeCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	oldQuestion := strings.Index(prompt, "怎么连接直播间？")
	oldAnswer := strings.Index(prompt, "先填写房间号并连接。")
	currentQuestion := strings.LastIndex(prompt, "怎么设置定时器？")
	lastUserTurn := strings.LastIndex(prompt, "<|im_start|>user\n")
	if oldQuestion >= 0 || oldAnswer >= 0 || currentQuestion < 0 || lastUserTurn < 0 || currentQuestion <= lastUserTurn {
		t.Fatalf("prompt must ignore conversation history and keep only the current user turn: %q", prompt)
	}
	if strings.Contains(prompt, "/no_think") {
		t.Fatalf("thinking mode must remain enabled: %q", prompt)
	}
	wantFinalTurn := "<|im_start|>user\n怎么设置定时器？<|im_end|>\n<|im_start|>assistant\n"
	if !strings.HasSuffix(prompt, wantFinalTurn) || strings.Contains(prompt[lastUserTurn:], "不要复述") || strings.Contains(prompt[lastUserTurn:], "直接回答下面") {
		t.Fatalf("the final user turn must contain only the current question: %q", prompt[lastUserTurn:])
	}
	if strings.Contains(prompt, "recentHistoryJSONL") || !strings.HasSuffix(prompt, "<|im_start|>assistant\n") {
		t.Fatalf("prompt must use ordered ChatML turns and end at the assistant turn: %q", prompt)
	}
}

func TestChatRejectsThinkingOnlyAnswer(t *testing.T) {
	engine := &fakeEngine{generate: func(_ context.Context, callback TokenCallback) error {
		return callback("<think>只有隐藏思考</think>")
	}}
	service := serviceWithActiveModel(t, engine, time.Hour)
	var events []StreamEvent
	err := service.Chat(context.Background(), ChatRequest{Question: "怎么连接直播间"}, func(event StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "未生成可见回答") {
		t.Fatalf("error = %v", err)
	}
	for _, event := range events {
		if event.Type == "done" {
			t.Fatalf("thinking-only answer must not finish successfully: %#v", events)
		}
	}
}

func TestOutOfScopeQuestionDoesNotInvokeEngine(t *testing.T) {
	engine := &fakeEngine{}
	service := serviceWithActiveModel(t, engine, time.Hour)
	var answer string
	err := service.Chat(context.Background(), ChatRequest{Question: "北京明天天气怎么样"}, func(event StreamEvent) error {
		if event.Type == "delta" {
			answer += event.Text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer != RefusalAnswer {
		t.Fatalf("answer = %q", answer)
	}
	if engine.loads != 0 || engine.generations != 0 {
		t.Fatalf("engine invoked: %#v", engine)
	}
}

func TestChatCancellationAndSingleConcurrency(t *testing.T) {
	started := make(chan struct{})
	engine := &fakeEngine{generate: func(ctx context.Context, _ TokenCallback) error { close(started); <-ctx.Done(); return ctx.Err() }}
	service := serviceWithActiveModel(t, engine, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Chat(ctx, ChatRequest{Question: "怎么连接直播间"}, func(StreamEvent) error { return nil })
	}()
	<-started
	if err := service.Chat(context.Background(), ChatRequest{Question: "属性怎么设置"}, func(StreamEvent) error { return nil }); err == nil || !strings.Contains(err.Error(), "正忙") {
		t.Fatalf("concurrent error = %v", err)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestIdleModelIsUnloaded(t *testing.T) {
	engine := &fakeEngine{}
	service := serviceWithActiveModel(t, engine, 20*time.Millisecond)
	if err := service.Chat(context.Background(), ChatRequest{Question: "房间号在哪里看"}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		engine.mu.Lock()
		unloaded := engine.unloads
		engine.mu.Unlock()
		if unloaded > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("idle model was not unloaded")
}

func TestMinimumAppVersion(t *testing.T) {
	if err := requireAppVersion("0.2.4", "0.3.0"); err == nil {
		t.Fatal("old app accepted")
	}
	if err := requireAppVersion("0.3.1", "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if err := requireAppVersion("dev", "99.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestThinkFilterPreservesUTF8AcrossOrdinaryChunks(t *testing.T) {
	var output strings.Builder
	filter := newThinkFilter(func(text string) error {
		output.WriteString(text)
		return nil
	})
	for _, chunk := range []string{"步", "骤1", " / ", "OBS", "设置"} {
		if err := filter.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := filter.Close(); err != nil {
		t.Fatal(err)
	}
	if answer := output.String(); answer != "步骤1 / OBS设置" || !utf8.ValidString(answer) {
		t.Fatalf("answer = %q, valid UTF-8 = %v", answer, utf8.ValidString(answer))
	}
}

func TestThinkFilterHandlesMarkersSplitAcrossChunks(t *testing.T) {
	var output strings.Builder
	filter := newThinkFilter(func(text string) error {
		output.WriteString(text)
		return nil
	})
	for _, chunk := range []string{"开头<thi", "nk>隐藏内容</th", "ink>步骤1"} {
		if err := filter.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := filter.Close(); err != nil {
		t.Fatal(err)
	}
	if answer := output.String(); answer != "开头步骤1" || !utf8.ValidString(answer) {
		t.Fatalf("answer = %q, valid UTF-8 = %v", answer, utf8.ValidString(answer))
	}
}
