package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type LanguageToolChecker struct {
	baseURL    string
	httpClient *http.Client
}

func NewLanguageToolChecker(baseURL string) *LanguageToolChecker {
	return &LanguageToolChecker{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type languageToolResponse struct {
	Language struct {
		Name string `json:"name"`
		Code string `json:"code"`
	} `json:"language"`
	Matches []languageToolMatch `json:"matches"`
}

type languageToolMatch struct {
	Message      string `json:"message"`
	ShortMessage string `json:"shortMessage"`
	Offset       int    `json:"offset"`
	Length       int    `json:"length"`
	Rule         struct {
		ID       string `json:"id"`
		Category struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"category"`
	} `json:"rule"`
	Replacements []struct {
		Value string `json:"value"`
	} `json:"replacements"`
}

func (c *LanguageToolChecker) Check(ctx context.Context, text, language string) (*CheckResult, error) {
	if language == "" {
		language = "auto"
	}

	form := url.Values{}
	form.Set("text", text)
	form.Set("language", language)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/check", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling LanguageTool: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LanguageTool returned status %d", resp.StatusCode)
	}

	var ltResp languageToolResponse
	if err := json.NewDecoder(resp.Body).Decode(&ltResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	annotations := make([]Annotation, 0, len(ltResp.Matches))
	for _, m := range ltResp.Matches {
		a := Annotation{
			Offset:       m.Offset,
			Length:       m.Length,
			Message:      m.Message,
			ShortMessage: m.ShortMessage,
			RuleID:       m.Rule.ID,
			Category:     m.Rule.Category.Name,
			Type:         mapCategory(m.Rule.Category.ID),
		}

		limit := len(m.Replacements)
		if limit > 5 {
			limit = 5
		}
		if limit > 0 {
			a.Replacements = make([]string, limit)
			for i := 0; i < limit; i++ {
				a.Replacements[i] = m.Replacements[i].Value
			}
		}

		annotations = append(annotations, a)
	}

	return &CheckResult{
		Annotations: annotations,
		Language:    ltResp.Language.Code,
	}, nil
}

func mapCategory(categoryID string) string {
	switch categoryID {
	case "TYPOS", "SPELL":
		return "spelling"
	case "GRAMMAR":
		return "grammar"
	case "STYLE":
		return "style"
	case "TYPOGRAPHY":
		return "typographical"
	default:
		return "grammar"
	}
}
