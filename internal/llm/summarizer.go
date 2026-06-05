package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/store/ai_conversation"
	log "github.com/sirupsen/logrus"
)

type Summarizer struct {
	client     *Client
	store      *ai_conversation.Store
	threshold  int
	keepRecent int
}

func NewSummarizer(client *Client, store *ai_conversation.Store) *Summarizer {
	return &Summarizer{
		client:     client,
		store:      store,
		threshold:  20,
		keepRecent: 6,
	}
}

// SetClient updates the LLM client (called after hot-reload).
func (s *Summarizer) SetClient(client *Client) {
	s.client = client
}

// MaybeSummarize compresses older messages if the conversation exceeds the threshold.
func (s *Summarizer) MaybeSummarize(ctx context.Context, conversationID, userEmail, model string) {
	count, err := s.store.CountMessages(ctx, conversationID)
	if err != nil {
		log.WithError(err).Warn("summarizer: count messages failed")
		return
	}
	if count <= s.threshold {
		return
	}

	msgs, err := s.store.GetMessages(ctx, conversationID, userEmail)
	if err != nil {
		log.WithError(err).Warn("summarizer: get messages failed")
		return
	}

	if len(msgs) <= s.keepRecent {
		return
	}

	toSummarize := msgs[:len(msgs)-s.keepRecent]
	cutoff := msgs[len(msgs)-s.keepRecent].CreatedAt

	var sb strings.Builder
	for _, m := range toSummarize {
		fmt.Fprintf(&sb, "%s: %s\n\n", m.Role, m.Content)
	}

	systemPrompt := "Summarize the following conversation between a user and an AI assistant. " +
		"Capture the key topics discussed, decisions made, and any important context. " +
		"Be concise but preserve essential information needed to continue the conversation coherently."

	if s.client == nil || !s.client.HasProviders() {
		return
	}

	summary, _, err := s.client.Do(ctx, model, systemPrompt, sb.String())
	if err != nil {
		log.WithError(err).Warn("summarizer: LLM call failed")
		return
	}

	deleted, err := s.store.DeleteMessagesBefore(ctx, conversationID, cutoff)
	if err != nil {
		log.WithError(err).Warn("summarizer: delete old messages failed")
		return
	}

	_, err = s.store.AddMessage(ctx, ai_conversation.Message{
		ConversationID: conversationID,
		Role:           "summary",
		Content:        summary,
	})
	if err != nil {
		log.WithError(err).Warn("summarizer: add summary message failed")
		return
	}

	log.WithFields(log.Fields{
		"conversation": conversationID,
		"deleted":      deleted,
		"kept":         s.keepRecent,
	}).Info("summarized conversation history")
}
