package httpapi

import (
	"errors"
	"testing"
)

func TestRequestIDFromReaderFailsWhenRandomSourceFails(t *testing.T) {
	_, err := requestIDFromReader(func([]byte) (int, error) {
		return 0, errors.New("random source unavailable")
	})
	if err == nil {
		t.Fatal("requestIDFromReader() error = nil, want random-source failure")
	}
}
