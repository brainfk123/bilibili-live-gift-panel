//go:build !windows || !cgo || !llamacpp

package llamacpp

import "bilibili-live-gift-panel/assistant"

// New returns an explicit unavailable Engine unless the Windows cgo adapter was
// included with the llamacpp build tag. Keeping this as the default makes
// ordinary development and go test independent of a native toolchain.
func New() assistant.Engine {
	return assistant.UnavailableEngine{
		Reason: "当前版本未包含本地答疑模型运行时（需要 Windows x64、cgo 和 llamacpp 构建标签）",
	}
}
