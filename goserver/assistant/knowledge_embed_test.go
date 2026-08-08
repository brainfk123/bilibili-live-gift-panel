package assistant

import "testing"

func TestEmbeddedKnowledgeBuildsKnowledgeBase(t *testing.T) {
	data := EmbeddedKnowledge()
	if len(data) == 0 {
		t.Fatal("EmbeddedKnowledge returned no data")
	}
	if _, err := NewKnowledgeBase(data); err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	data[0] = '!'
	if EmbeddedKnowledge()[0] == '!' {
		t.Fatal("EmbeddedKnowledge exposed mutable process-wide data")
	}
}
