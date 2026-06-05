package embedder

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

const (
	clsToken  = "[CLS]"
	sepToken  = "[SEP]"
	unkToken  = "[UNK]"
	padToken  = "[PAD]"
	maxSeqLen = 128
)

// Tokenizer is a WordPiece tokenizer compatible with BERT-based models.
type Tokenizer struct {
	vocab   map[string]int32
	idToTok map[int32]string
	clsID   int32
	sepID   int32
	unkID   int32
	padID   int32
}

// TokenizerOutput holds the three tensors expected by BERT models.
type TokenizerOutput struct {
	InputIDs      []int64
	AttentionMask []int64
	TokenTypeIDs  []int64
}

// NewTokenizer loads a vocab.txt file and builds the tokenizer.
func NewTokenizer(vocabPath string) (*Tokenizer, error) {
	f, err := os.Open(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("opening vocab: %w", err)
	}
	defer f.Close()

	vocab := make(map[string]int32)
	idToTok := make(map[int32]string)
	scanner := bufio.NewScanner(f)
	var idx int32
	for scanner.Scan() {
		tok := scanner.Text()
		vocab[tok] = idx
		idToTok[idx] = tok
		idx++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading vocab: %w", err)
	}

	getID := func(tok string) int32 {
		id, ok := vocab[tok]
		if !ok {
			return 0
		}
		return id
	}

	return &Tokenizer{
		vocab:   vocab,
		idToTok: idToTok,
		clsID:   getID(clsToken),
		sepID:   getID(sepToken),
		unkID:   getID(unkToken),
		padID:   getID(padToken),
	}, nil
}

// Tokenize converts text into token IDs with [CLS] and [SEP] tokens.
func (t *Tokenizer) Tokenize(text string) TokenizerOutput {
	// Lowercase
	text = strings.ToLower(text)

	// Split into words (on whitespace and punctuation)
	words := tokenizeBasic(text)

	// WordPiece tokenization
	var tokens []int32
	tokens = append(tokens, t.clsID)

	for _, word := range words {
		subTokens := t.wordPieceTokenize(word)
		if len(tokens)+len(subTokens)+1 > maxSeqLen { // +1 for [SEP]
			break
		}
		tokens = append(tokens, subTokens...)
	}

	tokens = append(tokens, t.sepID)

	// Create output tensors
	seqLen := len(tokens)
	out := TokenizerOutput{
		InputIDs:      make([]int64, maxSeqLen),
		AttentionMask: make([]int64, maxSeqLen),
		TokenTypeIDs:  make([]int64, maxSeqLen),
	}

	for i := 0; i < seqLen; i++ {
		out.InputIDs[i] = int64(tokens[i])
		out.AttentionMask[i] = 1
	}
	// Remaining positions stay 0 (pad token ID is 0, attention 0, type 0)

	return out
}

// TokenizeBatch tokenizes multiple texts.
func (t *Tokenizer) TokenizeBatch(texts []string) []TokenizerOutput {
	results := make([]TokenizerOutput, len(texts))
	for i, text := range texts {
		results[i] = t.Tokenize(text)
	}
	return results
}

func (t *Tokenizer) wordPieceTokenize(word string) []int32 {
	if _, ok := t.vocab[word]; ok {
		return []int32{t.vocab[word]}
	}

	var tokens []int32
	start := 0
	for start < len(word) {
		end := len(word)
		var bestToken string
		var bestID int32
		found := false

		for end > start {
			substr := word[start:end]
			if start > 0 {
				substr = "##" + substr
			}
			if id, ok := t.vocab[substr]; ok {
				bestToken = substr
				bestID = id
				found = true
				break
			}
			end--
		}

		if !found {
			tokens = append(tokens, t.unkID)
			break
		}

		_ = bestToken
		tokens = append(tokens, bestID)
		start = end
	}

	return tokens
}

// tokenizeBasic splits text on whitespace and punctuation, keeping punctuation as separate tokens.
func tokenizeBasic(text string) []string {
	var words []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
			continue
		}
		if unicode.IsPunct(r) || isCJK(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
			words = append(words, string(r))
			continue
		}
		current.WriteRune(r)
	}

	if current.Len() > 0 {
		words = append(words, current.String())
	}

	return words
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x2F800 && r <= 0x2FA1F)
}
