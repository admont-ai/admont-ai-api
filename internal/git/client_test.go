package git

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFileChange_String(t *testing.T) {
	fc := FileChange{
		CommitHash:  "abc123",
		Author:      "Alice",
		AuthorEmail: "alice@example.com",
		Date:        time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Message:     "fix typo",
		Diff:        "- old\n+ new",
	}

	result := fc.String()
	assert.Contains(t, result, "abc123")
	assert.Contains(t, result, "Alice")
	assert.Contains(t, result, "alice@example.com")
	assert.Contains(t, result, "fix typo")
	assert.Contains(t, result, "- old\n+ new")
	assert.Contains(t, result, "2024-01-15")
}

func TestScanCRLF(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		atEOF   bool
		advance int
		token   string
		wantNil bool
	}{
		{"LF ending", []byte("hello\nworld"), false, 6, "hello", false},
		{"CR LF ending", []byte("hello\r\nworld"), false, 7, "hello", false},
		{"CR only ending", []byte("hello\rworld"), false, 6, "hello", false},
		{"EOF with data", []byte("last line"), true, 9, "last line", false},
		{"EOF empty", []byte{}, true, 0, "", true},
		{"need more data", []byte("incomplete"), false, 0, "", true},
		{"just LF", []byte("\nrest"), false, 1, "", false},
		{"just CR LF", []byte("\r\nrest"), false, 2, "", false},
		{"just CR", []byte("\rrest"), false, 1, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advance, token, err := scanCRLF(tt.input, tt.atEOF)
			assert.NoError(t, err)
			assert.Equal(t, tt.advance, advance)
			if tt.wantNil {
				assert.Nil(t, token)
			} else {
				assert.Equal(t, tt.token, string(token))
			}
		})
	}
}

func TestPorcelainCodeString(t *testing.T) {
	tests := []struct {
		code byte
		want string
	}{
		{'M', "modified"},
		{'A', "added"},
		{'D', "deleted"},
		{'R', "renamed"},
		{'C', "copied"},
		{'U', "unmerged"},
		{'?', "untracked"},
		{' ', ""},
		{'X', "X"},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			assert.Equal(t, tt.want, porcelainCodeString(tt.code))
		})
	}
}

func TestIsValidCommitRef(t *testing.T) {
	tests := []struct {
		name  string
		ref   string
		valid bool
	}{
		{"full SHA-1", "abc123def456abc123def456abc123def456abc1", true},
		{"short hash", "abc123", true},
		{"minimum length", "a", true},
		{"uppercase hex", "ABCDEF1234", true},
		{"mixed case", "aAbBcC1234", true},
		{"empty", "", false},
		{"too long", "abc123def456abc123def456abc123def456abc12x", false},
		{"flag injection", "--help", false},
		{"flag with value", "-c", false},
		{"contains non-hex", "xyz123", false},
		{"contains dot", "abc.123", false},
		{"contains colon", "abc:123", false},
		{"contains slash", "abc/123", false},
		{"contains space", "abc 123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.valid, isValidCommitRef(tt.ref))
		})
	}
}

func TestClient_AuthURL(t *testing.T) {
	tests := []struct {
		name      string
		repoUrl   string
		username  string
		authToken string
		want      string
	}{
		{
			"no credentials",
			"https://github.com/org/repo.git",
			"", "",
			"https://github.com/org/repo.git",
		},
		{
			"with credentials",
			"https://github.com/org/repo.git",
			"bot", "ghp_token123",
			"https://bot:ghp_token123@github.com/org/repo.git",
		},
		{
			"token only",
			"https://github.com/org/repo.git",
			"", "ghp_token123",
			"https://:ghp_token123@github.com/org/repo.git",
		},
		{
			"relative URL gets credentials",
			"not-a-url",
			"user", "pass",
			"//user:pass@not-a-url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				repoUrl:   tt.repoUrl,
				username:  tt.username,
				authToken: tt.authToken,
			}
			assert.Equal(t, tt.want, c.authURL())
		})
	}
}
