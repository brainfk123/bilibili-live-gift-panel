//go:build windows && cgo && llamacpp

package llamacpp

/*
#cgo CFLAGS: -std=c11 -I${SRCDIR}/../../../.native/llama.cpp/windows-amd64/include
#cgo LDFLAGS: ${SRCDIR}/../../../.native/llama.cpp/windows-amd64/lib/libllama.a ${SRCDIR}/../../../.native/llama.cpp/windows-amd64/lib/ggml.a ${SRCDIR}/../../../.native/llama.cpp/windows-amd64/lib/ggml-cpu.a ${SRCDIR}/../../../.native/llama.cpp/windows-amd64/lib/ggml-base.a -static-libgcc -static-libstdc++ -lstdc++ -lws2_32
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"bilibili-live-gift-panel/assistant"
)

const tokenPieceCapacity = 4096

type engine struct {
	native *C.blgp_llama_engine
}

var _ assistant.Engine = (*engine)(nil)

func New() assistant.Engine {
	return &engine{}
}

func (engine *engine) Load(ctx context.Context, modelPath string, options assistant.LoadOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.ContextSize < 256 {
		return fmt.Errorf("llama.cpp: context size must be at least 256")
	}
	if options.Threads < 1 || options.Threads > 4 {
		return fmt.Errorf("llama.cpp: thread count must be between 1 and 4")
	}
	if err := engine.Unload(); err != nil {
		return err
	}
	engine.native = C.blgp_llama_new()
	if engine.native == nil {
		return fmt.Errorf("llama.cpp: unable to allocate native engine")
	}

	path := C.CString(modelPath)
	defer C.free(unsafe.Pointer(path))
	stopCancellation := engine.watchCancellation(ctx)
	result := C.blgp_llama_load(engine.native, path, C.int32_t(options.ContextSize), C.int32_t(options.Threads))
	stopCancellation()
	runtime.KeepAlive(engine)
	if err := ctx.Err(); err != nil {
		_ = engine.Unload()
		return err
	}
	if result != 0 {
		err := engine.nativeError("load model")
		_ = engine.Unload()
		return err
	}
	return nil
}

func (engine *engine) Generate(ctx context.Context, request assistant.GenerateRequest, onToken assistant.TokenCallback) error {
	if engine.native == nil {
		return &assistant.EngineUnavailableError{Reason: "本地答疑模型尚未加载"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Prompt == "" {
		return errors.New("llama.cpp: prompt must not be empty")
	}
	if request.MaxTokens < 1 || request.MaxTokens > 384 {
		return errors.New("llama.cpp: max tokens must be between 1 and 384")
	}
	if request.Temperature <= 0 || request.TopP <= 0 || request.TopP > 1 || request.TopK < 1 {
		return errors.New("llama.cpp: invalid sampling options")
	}
	if onToken == nil {
		return errors.New("llama.cpp: token callback must not be nil")
	}

	prompt := C.CString(request.Prompt)
	defer C.free(unsafe.Pointer(prompt))
	stopCancellation := engine.watchCancellation(ctx)
	defer stopCancellation()
	if result := C.blgp_llama_generate_start(
		engine.native,
		prompt,
		C.int32_t(len([]byte(request.Prompt))),
		C.int32_t(request.MaxTokens),
		C.float(request.Temperature),
		C.float(request.TopP),
		C.int32_t(request.TopK),
	); result != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return engine.nativeError("start generation")
	}

	buffer := make([]byte, tokenPieceCapacity)
	pending := make([]byte, 0, 8)
	for {
		count := C.blgp_llama_next(
			engine.native,
			(*C.char)(unsafe.Pointer(&buffer[0])),
			C.int32_t(len(buffer)),
		)
		runtime.KeepAlive(engine)
		if count < 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			return engine.nativeError("generate token")
		}
		if count == 0 {
			if len(pending) > 0 {
				if err := onToken(string([]rune(string(pending)))); err != nil {
					return err
				}
			}
			return nil
		}
		pending = append(pending, buffer[:int(count)]...)
		emitBytes, remainder := completeUTF8Prefix(pending)
		if len(emitBytes) > 0 {
			if err := onToken(string(emitBytes)); err != nil {
				C.blgp_llama_abort(engine.native)
				return err
			}
		}
		pending = remainder
	}
}

func (engine *engine) Unload() error {
	if engine.native != nil {
		C.blgp_llama_delete(engine.native)
		engine.native = nil
	}
	return nil
}

func (engine *engine) watchCancellation(ctx context.Context) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func(native *C.blgp_llama_engine) {
		defer close(finished)
		select {
		case <-ctx.Done():
			C.blgp_llama_abort(native)
		case <-done:
		}
	}(engine.native)
	return func() {
		close(done)
		<-finished
	}
}

func (engine *engine) nativeError(operation string) error {
	if engine.native == nil {
		return fmt.Errorf("llama.cpp %s: native engine is unavailable", operation)
	}
	return fmt.Errorf("llama.cpp %s: %s", operation, C.GoString(C.blgp_llama_last_error(engine.native)))
}
