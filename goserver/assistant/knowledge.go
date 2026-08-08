package assistant

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const maximumSources = 4

var safeTrainingTarget = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type indexedEntry struct {
	entry  HelpEntry
	tokens map[string]int
	length int
}

type KnowledgeBase struct {
	entries       []indexedEntry
	documentFreq  map[string]int
	averageLength float64
	scopePhrases  []string
}

type SearchResult struct {
	Entry HelpEntry
	Score float64
}

func NewKnowledgeBase(data []byte) (*KnowledgeBase, error) {
	var entries []HelpEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("解析帮助内容失败：%w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("帮助内容不能为空")
	}
	seen := make(map[string]struct{}, len(entries))
	scopePhrases := map[string]struct{}{}
	base := &KnowledgeBase{documentFreq: map[string]int{}}
	for _, entry := range entries {
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Title = strings.TrimSpace(entry.Title)
		entry.Content = strings.TrimSpace(entry.canonicalContent())
		entry.SourceLabel = strings.TrimSpace(entry.SourceLabel)
		if entry.ID == "" || entry.Title == "" || entry.Content == "" || entry.SourceLabel == "" {
			return nil, fmt.Errorf("帮助条目缺少 id、title、content 或 sourceLabel")
		}
		if _, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("帮助条目 id 重复：%s", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		if err := validateAction(entry.Action); err != nil {
			return nil, fmt.Errorf("帮助条目 %s：%w", entry.ID, err)
		}
		for _, keyword := range entry.Keywords {
			keyword = strings.ToLower(strings.TrimSpace(keyword))
			if len([]rune(keyword)) >= 2 {
				scopePhrases[keyword] = struct{}{}
			}
		}
		weighted := entry.Title + " " + entry.Title + " " + strings.Join(entry.QuestionVariants, " ") +
			" " + strings.Join(entry.QuestionVariants, " ") + " " + strings.Join(entry.Keywords, " ") +
			" " + strings.Join(entry.Keywords, " ") + " " + strings.Join(entry.Keywords, " ") + " " + entry.Content
		tokens := tokenCounts(weighted)
		indexed := indexedEntry{entry: entry, tokens: tokens}
		for token, count := range tokens {
			indexed.length += count
			base.documentFreq[token]++
		}
		base.entries = append(base.entries, indexed)
		base.averageLength += float64(indexed.length)
	}
	base.averageLength /= float64(len(base.entries))
	for phrase := range scopePhrases {
		base.scopePhrases = append(base.scopePhrases, phrase)
	}
	sort.Strings(base.scopePhrases)
	return base, nil
}

func validateAction(action *Action) error {
	if action == nil {
		return nil
	}
	action.Kind = strings.TrimSpace(action.Kind)
	action.Target = strings.TrimSpace(action.Target)
	switch action.Kind {
	case "config-page":
		switch action.Target {
		case "overview", "attributes", "activities", "kpi", "obs", "data":
			return nil
		default:
			return fmt.Errorf("不安全的配置页动作 %q", action.Target)
		}
	case "training-topic":
		if safeTrainingTarget.MatchString(action.Target) {
			return nil
		}
		return fmt.Errorf("不安全的教程动作 %q", action.Target)
	default:
		return fmt.Errorf("不支持的动作类型 %q", action.Kind)
	}
}

func (base *KnowledgeBase) Search(query string, limit int) []SearchResult {
	if !base.inScope(query) {
		return nil
	}
	queryTokens := tokenCounts(query)
	if len(queryTokens) == 0 || len(base.entries) == 0 {
		return nil
	}
	if limit <= 0 || limit > maximumSources {
		limit = maximumSources
	}
	const k1 = 1.2
	const b = 0.75
	results := make([]SearchResult, 0, len(base.entries))
	for _, document := range base.entries {
		score := 0.0
		matchedQueryTerms := 0
		for token := range queryTokens {
			frequency := document.tokens[token]
			if frequency == 0 {
				continue
			}
			matchedQueryTerms++
			idf := math.Log(1 + (float64(len(base.entries)-base.documentFreq[token])+0.5)/(float64(base.documentFreq[token])+0.5))
			normalized := float64(frequency) * (k1 + 1) /
				(float64(frequency) + k1*(1-b+b*float64(document.length)/base.averageLength))
			score += idf * normalized
		}
		coverage := float64(matchedQueryTerms) / float64(len(queryTokens))
		if score >= 1.5 && coverage >= 0.16 {
			results = append(results, SearchResult{Entry: document.entry, Score: score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Entry.ID < results[j].Entry.ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (base *KnowledgeBase) inScope(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, phrase := range base.scopePhrases {
		if strings.Contains(query, phrase) {
			return true
		}
	}
	return false
}

func tokenCounts(value string) map[string]int {
	value = strings.ToLower(strings.TrimSpace(value))
	counts := map[string]int{}
	var latin strings.Builder
	var han []rune
	flushLatin := func() {
		if latin.Len() > 0 {
			counts[latin.String()]++
			latin.Reset()
		}
	}
	flushHan := func() {
		for _, char := range han {
			counts[string(char)]++
		}
		for index := 0; index+1 < len(han); index++ {
			counts[string(han[index:index+2])] += 2
		}
		han = han[:0]
	}
	for _, char := range []rune(value) {
		switch {
		case unicode.Is(unicode.Han, char):
			flushLatin()
			han = append(han, char)
		case unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-':
			flushHan()
			latin.WriteRune(char)
		default:
			flushLatin()
			flushHan()
		}
	}
	flushLatin()
	flushHan()
	return counts
}
