package assistant

import "context"

// LoadOptions contains the deliberately small set of runtime knobs exposed by
// the assistant service. Implementations must not silently enable a GPU.
type LoadOptions struct {
	ContextSize int
	Threads     int
}

// GenerateRequest is independent of any particular native inference library.
type GenerateRequest struct {
	Prompt      string
	MaxTokens   int
	Temperature float32
	TopP        float32
	TopK        int
}

// TokenCallback receives complete UTF-8 text fragments. Returning an error
// stops generation.
type TokenCallback func(text string) error

// Engine is the seam between the assistant service and the native runtime.
// A Service serializes all calls, so implementations need not support
// concurrent Load, Generate, or Unload calls.
type Engine interface {
	Load(ctx context.Context, modelPath string, options LoadOptions) error
	Generate(ctx context.Context, request GenerateRequest, onToken TokenCallback) error
	Unload() error
}

// UnavailableEngine keeps non-cgo builds functional while making the missing
// native runtime explicit to the UI.
type UnavailableEngine struct {
	Reason string
}

func (engine UnavailableEngine) Load(context.Context, string, LoadOptions) error {
	return &EngineUnavailableError{Reason: engine.Reason}
}

func (UnavailableEngine) Generate(context.Context, GenerateRequest, TokenCallback) error {
	return &EngineUnavailableError{}
}

func (UnavailableEngine) Unload() error { return nil }

type EngineUnavailableError struct {
	Reason string
}

func (err *EngineUnavailableError) Error() string {
	if err.Reason != "" {
		return err.Reason
	}
	return "当前版本未包含本地答疑模型运行时"
}
