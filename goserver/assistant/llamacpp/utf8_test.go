package llamacpp

import (
	"bytes"
	"testing"
)

func TestCompleteUTF8PrefixKeepsPartialRune(t *testing.T) {
	value := []byte("回答")
	cut := len(value) - 1
	complete, remainder := completeUTF8Prefix(value[:cut])
	if string(complete) != "回" {
		t.Fatalf("complete = %q, want 回", complete)
	}
	if !bytes.Equal(remainder, value[len([]byte("回")):cut]) {
		t.Fatalf("remainder = %v, want partial rune", remainder)
	}
}
