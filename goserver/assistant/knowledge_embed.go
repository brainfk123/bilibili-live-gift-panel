package assistant

import _ "embed"

//go:embed help-content.generated.json
var embeddedKnowledge []byte

// EmbeddedKnowledge returns an isolated copy of the authoritative help data so
// callers cannot mutate the process-wide embedded bytes.
func EmbeddedKnowledge() []byte {
	return append([]byte(nil), embeddedKnowledge...)
}
