package embedder

import (
	"fmt"
	"math"

	"github.com/pgvector/pgvector-go"
	ort "github.com/yalue/onnxruntime_go"
)

const embeddingDim = 384

// Embedder wraps ONNX Runtime for generating text embeddings.
type Embedder struct {
	tokenizer *Tokenizer
	session   *ort.DynamicAdvancedSession
}

// New creates an Embedder with the given ONNX Runtime library, model, and vocab paths.
func New(onnxRuntimePath, modelPath, vocabPath string) (*Embedder, error) {
	ort.SetSharedLibraryPath(onnxRuntimePath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("initializing ONNX Runtime: %w", err)
	}

	tokenizer, err := NewTokenizer(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("creating tokenizer: %w", err)
	}

	inputNames := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputNames := []string{"last_hidden_state"}

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		inputNames,
		outputNames,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("creating ONNX session: %w", err)
	}

	return &Embedder{
		tokenizer: tokenizer,
		session:   session,
	}, nil
}

// Embed generates a single embedding vector for the given text.
func (e *Embedder) Embed(text string) (pgvector.Vector, error) {
	vecs, err := e.EmbedBatch([]string{text})
	if err != nil {
		return pgvector.Vector{}, err
	}
	return vecs[0], nil
}

// EmbedBatch generates embedding vectors for multiple texts.
func (e *Embedder) EmbedBatch(texts []string) ([]pgvector.Vector, error) {
	batchSize := int64(len(texts))
	seqLen := int64(maxSeqLen)

	tokenOutputs := e.tokenizer.TokenizeBatch(texts)

	// Flatten batch into single arrays
	inputIDs := make([]int64, batchSize*seqLen)
	attentionMask := make([]int64, batchSize*seqLen)
	tokenTypeIDs := make([]int64, batchSize*seqLen)

	for i, out := range tokenOutputs {
		offset := int64(i) * seqLen
		copy(inputIDs[offset:offset+seqLen], out.InputIDs)
		copy(attentionMask[offset:offset+seqLen], out.AttentionMask)
		copy(tokenTypeIDs[offset:offset+seqLen], out.TokenTypeIDs)
	}

	shape := ort.Shape{batchSize, seqLen}

	inputIDsTensor, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer inputIDsTensor.Destroy()

	attMaskTensor, err := ort.NewTensor(shape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer attMaskTensor.Destroy()

	typeTensor, err := ort.NewTensor(shape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("creating token_type_ids tensor: %w", err)
	}
	defer typeTensor.Destroy()

	outputShape := ort.Shape{batchSize, seqLen, int64(embeddingDim)}
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		return nil, fmt.Errorf("creating output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	err = e.session.Run(
		[]ort.ArbitraryTensor{inputIDsTensor, attMaskTensor, typeTensor},
		[]ort.ArbitraryTensor{outputTensor},
	)
	if err != nil {
		return nil, fmt.Errorf("running inference: %w", err)
	}

	outputData := outputTensor.GetData()

	// Mean pooling over non-padding tokens + L2 normalize
	vectors := make([]pgvector.Vector, batchSize)
	for i := int64(0); i < batchSize; i++ {
		vec := meanPool(outputData, attentionMask, i, seqLen, int64(embeddingDim))
		l2Normalize(vec)
		vectors[i] = pgvector.NewVector(vec)
	}

	return vectors, nil
}

func meanPool(output []float32, mask []int64, batch, seqLen, dim int64) []float32 {
	result := make([]float32, dim)
	var count float32

	for t := int64(0); t < seqLen; t++ {
		if mask[batch*seqLen+t] == 0 {
			continue
		}
		count++
		offset := batch*seqLen*dim + t*dim
		for d := int64(0); d < dim; d++ {
			result[d] += output[offset+d]
		}
	}

	if count > 0 {
		for d := int64(0); d < dim; d++ {
			result[d] /= count
		}
	}

	return result
}

func l2Normalize(vec []float32) {
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
}

// Close cleans up ONNX Runtime resources.
func (e *Embedder) Close() error {
	if e.session != nil {
		e.session.Destroy()
	}
	return ort.DestroyEnvironment()
}
