package checker

import "context"

type Annotation struct {
	Offset       int      `json:"offset"`
	Length       int      `json:"length"`
	Message      string   `json:"message"`
	ShortMessage string   `json:"short_message,omitempty"`
	RuleID       string   `json:"rule_id,omitempty"`
	Category     string   `json:"category,omitempty"`
	Type         string   `json:"type"`
	Replacements []string `json:"replacements,omitempty"`
}

type CheckResult struct {
	Annotations []Annotation `json:"annotations"`
	Language    string       `json:"language,omitempty"`
}

type Checker interface {
	Check(ctx context.Context, text, language string) (*CheckResult, error)
}
