package embedder

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizeBasic_Patterns(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"with punctuation", "hello, world!", []string{"hello", ",", "world", "!"}},
		{"multiple spaces", "hello   world", []string{"hello", "world"}},
		{"empty string", "", nil},
		{"whitespace only", "   \t\n  ", nil},
		{"with dots", "e.g. something", []string{"e", ".", "g", ".", "something"}},
		{"parentheses", "func(x)", []string{"func", "(", "x", ")"}},
		{"hyphenated", "state-of-the-art", []string{"state", "-", "of", "-", "the", "-", "art"}},
		{"numbers and letters", "abc123", []string{"abc123"}},
		{"CJK characters", "你好世界", []string{"你", "好", "世", "界"}},
		{"mixed CJK and latin", "hello世界test", []string{"hello", "世", "界", "test"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenizeBasic(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCJK(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"common CJK", '中', true},
		{"hiragana-like CJK", '你', true},
		{"latin letter", 'A', false},
		{"digit", '0', false},
		{"punctuation", '.', false},
		{"CJK boundary low", '一', true},
		{"CJK boundary high", '鿿', true},
		{"just below CJK", '䷿', false},
		{"CJK Extension A", '㐀', true},
		{"CJK Compat", '豈', true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isCJK(tt.r))
		})
	}
}

func TestL2Normalize(t *testing.T) {
	vec := []float32{3.0, 4.0}
	l2Normalize(vec)

	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	assert.InDelta(t, 1.0, norm, 1e-6, "normalized vector should have unit length")

	assert.InDelta(t, 0.6, vec[0], 1e-6)
	assert.InDelta(t, 0.8, vec[1], 1e-6)
}

func TestL2Normalize_ZeroVector(t *testing.T) {
	vec := []float32{0.0, 0.0, 0.0}
	l2Normalize(vec)
	assert.Equal(t, float32(0.0), vec[0])
	assert.Equal(t, float32(0.0), vec[1])
}

func TestL2Normalize_SingleElement(t *testing.T) {
	vec := []float32{5.0}
	l2Normalize(vec)
	assert.InDelta(t, 1.0, vec[0], 1e-6)
}

func TestL2Normalize_NegativeValues(t *testing.T) {
	vec := []float32{-3.0, 4.0}
	l2Normalize(vec)

	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	assert.InDelta(t, 1.0, norm, 1e-6)
}

func TestMeanPool(t *testing.T) {
	seqLen := int64(4)
	dim := int64(3)

	output := []float32{
		1, 2, 3,
		4, 5, 6,
		7, 8, 9,
		10, 11, 12,
	}
	mask := []int64{1, 1, 0, 0}

	result := meanPool(output, mask, 0, seqLen, dim)
	assert.Len(t, result, int(dim))

	assert.InDelta(t, 2.5, result[0], 1e-6)
	assert.InDelta(t, 3.5, result[1], 1e-6)
	assert.InDelta(t, 4.5, result[2], 1e-6)
}

func TestMeanPool_AllMasked(t *testing.T) {
	seqLen := int64(3)
	dim := int64(2)

	output := []float32{1, 2, 3, 4, 5, 6}
	mask := []int64{0, 0, 0}

	result := meanPool(output, mask, 0, seqLen, dim)
	assert.Len(t, result, int(dim))
	assert.Equal(t, float32(0), result[0])
	assert.Equal(t, float32(0), result[1])
}

func TestMeanPool_AllUnmasked(t *testing.T) {
	seqLen := int64(2)
	dim := int64(2)

	output := []float32{2, 4, 6, 8}
	mask := []int64{1, 1}

	result := meanPool(output, mask, 0, seqLen, dim)
	assert.InDelta(t, 4.0, result[0], 1e-6)
	assert.InDelta(t, 6.0, result[1], 1e-6)
}

func TestMeanPool_SecondBatch(t *testing.T) {
	seqLen := int64(2)
	dim := int64(2)

	output := []float32{
		1, 1, 1, 1,
		10, 20, 30, 40,
	}
	mask := []int64{
		1, 1,
		1, 1,
	}

	result := meanPool(output, mask, 1, seqLen, dim)
	assert.InDelta(t, 20.0, result[0], 1e-6)
	assert.InDelta(t, 30.0, result[1], 1e-6)
}
