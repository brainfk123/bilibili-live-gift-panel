package assistant

import (
	"context"
	"strings"
)

const (
	RefusalAnswer    = "这个问题超出了答疑助手的范围。我只能回答本项目的安装、登录、直播连接、配置、OBS、统计、更新和故障排查问题。"
	NoEvidenceAnswer = "现有帮助内容里没有足够依据回答这个问题。请换一种说法，或打开训练中心查看相关教程。"
)

type Action struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"`
}

type HelpEntry struct {
	ID               string   `json:"id"`
	Category         string   `json:"category"`
	Title            string   `json:"title"`
	Content          string   `json:"content"`
	Answer           string   `json:"answer,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	Steps            []string `json:"steps,omitempty"`
	Outcome          string   `json:"outcome,omitempty"`
	QuestionVariants []string `json:"questionVariants"`
	Keywords         []string `json:"keywords"`
	SourceLabel      string   `json:"sourceLabel"`
	Action           *Action  `json:"action,omitempty"`
}

func (entry HelpEntry) canonicalContent() string {
	if entry.Content != "" {
		return entry.Content
	}
	if entry.Answer != "" {
		return entry.Answer
	}
	value := strings.TrimSpace(entry.Summary)
	for _, step := range entry.Steps {
		if value != "" {
			value += "\n"
		}
		value += step
	}
	if outcome := strings.TrimSpace(entry.Outcome); outcome != "" {
		if value != "" {
			value += "\n"
		}
		value += outcome
	}
	return value
}

type Source struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	SourceLabel string  `json:"sourceLabel"`
	Action      *Action `json:"action,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type StateSummary struct {
	AppVersion     string `json:"appVersion"`
	Connection     string `json:"connection"`
	Login          string `json:"login"`
	RoomConfigured bool   `json:"roomConfigured"`
	AttributeCount int    `json:"attributeCount"`
	RuleCount      int    `json:"ruleCount"`
	TimerCount     int    `json:"timerCount"`
	CurrentError   string `json:"currentError,omitempty"`
}

// StateProvider must return an already-redacted snapshot. The assistant package
// intentionally has no access to the application's persisted configuration.
type StateProvider func(context.Context) (StateSummary, error)

type AssistantStatus struct {
	State           string  `json:"state"`
	ModelVersion    string  `json:"modelVersion,omitempty"`
	LatestVersion   string  `json:"latestVersion,omitempty"`
	Message         string  `json:"message"`
	Progress        float64 `json:"progress,omitempty"`
	SizeBytes       int64   `json:"sizeBytes,omitempty"`
	InstalledBytes  int64   `json:"installedBytes,omitempty"`
	UpdateAvailable bool    `json:"updateAvailable,omitempty"`
}
