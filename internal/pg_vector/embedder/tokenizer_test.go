package embedder

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenizeBasic(t *testing.T) {
	words := tokenizeBasic("hello world, how are you?")
	assert.Equal(t, []string{"hello", "world", ",", "how", "are", "you", "?"}, words)
}

func TestTokenizeBasic_Uppercase(t *testing.T) {
	// tokenizeBasic doesn't lowercase; the Tokenize method does
	words := tokenizeBasic("Hello World")
	assert.Equal(t, []string{"Hello", "World"}, words)
}

func TestTokenizerOutput_MaxSeqLen(t *testing.T) {
	// Create a minimal vocab for testing
	vocabPath := createTestVocab(t)
	defer os.Remove(vocabPath)

	tok, err := NewTokenizer(vocabPath)
	require.NoError(t, err)

	out := tok.Tokenize("hello world")

	assert.Len(t, out.InputIDs, maxSeqLen)
	assert.Len(t, out.AttentionMask, maxSeqLen)
	assert.Len(t, out.TokenTypeIDs, maxSeqLen)

	// First token should be [CLS]
	assert.Equal(t, int64(tok.clsID), out.InputIDs[0])
	assert.Equal(t, int64(1), out.AttentionMask[0])

	// Padding positions should have attention 0
	// Find first pad
	var lastNonPad int
	for i := len(out.AttentionMask) - 1; i >= 0; i-- {
		if out.AttentionMask[i] == 1 {
			lastNonPad = i
			break
		}
	}
	if lastNonPad < maxSeqLen-1 {
		assert.Equal(t, int64(0), out.AttentionMask[lastNonPad+1])
	}
}

func TestTokenizeBatch(t *testing.T) {
	vocabPath := createTestVocab(t)
	defer os.Remove(vocabPath)

	tok, err := NewTokenizer(vocabPath)
	require.NoError(t, err)

	results := tok.TokenizeBatch([]string{"hello", "world"})
	assert.Len(t, results, 2)
}

func createTestVocab(t *testing.T) string {
	t.Helper()

	// Minimal vocab with special tokens and common words
	vocab := []string{
		"[PAD]", // 0
		"[UNK]", // 1
		"[CLS]", // 2
		"[SEP]", // 3
		"hello", // 4
		"world", // 5
		"how",   // 6
		"are",   // 7
		"you",   // 8
		",",     // 9
		"?",     // 10
		"##s",   // 11
		"test",  // 12
		"##ing", // 13
	}

	f, err := os.CreateTemp("", "vocab-*.txt")
	require.NoError(t, err)

	for _, v := range vocab {
		_, err := f.WriteString(v + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, f.Close())

	return f.Name()
}
