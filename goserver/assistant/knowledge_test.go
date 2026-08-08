package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGoldenQuestionsRetrieveExpectedEntryInTopFour(t *testing.T) {
	data := EmbeddedKnowledge()
	var entries []HelpEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	base, err := NewKnowledgeBase(data)
	if err != nil {
		t.Fatal(err)
	}
	questions := 0
	misses := []string{}
	for _, entry := range entries {
		for _, question := range entry.QuestionVariants {
			questions++
			results := base.Search(question, 4)
			found := false
			for _, result := range results {
				if result.Entry.ID == entry.ID {
					found = true
					break
				}
			}
			if !found {
				misses = append(misses, entry.ID+": "+question)
			}
		}
	}
	if questions < 120 {
		t.Fatalf("golden questions = %d, want at least 120", questions)
	}
	recall := float64(questions-len(misses)) / float64(questions)
	if recall < .95 {
		t.Fatalf("top-4 recall = %.3f, misses:\n%s", recall, strings.Join(misses, "\n"))
	}
}

func TestSearchStrictlyRejectsUnrelatedQuestion(t *testing.T) {
	base, err := NewKnowledgeBase(EmbeddedKnowledge())
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"北京明天天气怎么样", "帮我写一首爱情诗", "Java 怎么实现红黑树", "今天买哪只股票"} {
		if results := base.Search(question, 4); len(results) != 0 {
			t.Errorf("unrelated question %q matched %#v", question, results)
		}
	}
}

func TestKnowledgeRejectsUnsafeActions(t *testing.T) {
	data := []byte(`[{"id":"bad","category":"x","title":"bad","content":"bad","sourceLabel":"test","action":{"kind":"config-page","target":"javascript:alert(1)"}}]`)
	if _, err := NewKnowledgeBase(data); err == nil {
		t.Fatal("unsafe action was accepted")
	}
}
