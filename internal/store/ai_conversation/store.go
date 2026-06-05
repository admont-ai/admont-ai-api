package ai_conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) CreateConversation(ctx context.Context, c Conversation) (*Conversation, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_conversations (user_email, title, scope, repo_slug, file_path)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at
	`, c.UserEmail, c.Title, c.Scope, c.RepoSlug, c.FilePath).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating conversation: %w", err)
	}
	return &c, nil
}

func (s *Store) GetConversation(ctx context.Context, id, userEmail string) (*Conversation, error) {
	var c Conversation
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_email, title, scope, repo_slug, file_path, created_at, updated_at
		FROM ai_conversations WHERE id = $1 AND user_email = $2
	`, id, userEmail).Scan(&c.ID, &c.UserEmail, &c.Title, &c.Scope, &c.RepoSlug, &c.FilePath, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting conversation %s: %w", id, err)
	}
	return &c, nil
}

func (s *Store) ListConversations(ctx context.Context, userEmail string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, title, scope, repo_slug, file_path, created_at, updated_at
		FROM ai_conversations
		WHERE user_email = $1
		ORDER BY updated_at DESC
		LIMIT $2 OFFSET $3
	`, userEmail, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing conversations: %w", err)
	}
	defer rows.Close()

	var convs []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Scope, &c.RepoSlug, &c.FilePath, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

func (s *Store) UpdateTitle(ctx context.Context, id, userEmail, title string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_conversations SET title = $3 WHERE id = $1 AND user_email = $2
	`, id, userEmail, title)
	if err != nil {
		return fmt.Errorf("updating conversation title: %w", err)
	}
	return nil
}

func (s *Store) TouchConversation(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE ai_conversations SET updated_at = NOW() WHERE id = $1
	`, id)
	return err
}

func (s *Store) DeleteConversation(ctx context.Context, id, userEmail string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ai_conversations WHERE id = $1 AND user_email = $2`, id, userEmail)
	if err != nil {
		return fmt.Errorf("deleting conversation %s: %w", id, err)
	}
	return nil
}

func (s *Store) AddMessage(ctx context.Context, m Message) (*Message, error) {
	sourcesJSON, err := json.Marshal(m.Sources)
	if err != nil {
		return nil, fmt.Errorf("marshaling sources: %w", err)
	}
	var usageJSON []byte
	if m.TokenUsage != nil {
		usageJSON, err = json.Marshal(m.TokenUsage)
		if err != nil {
			return nil, fmt.Errorf("marshaling token usage: %w", err)
		}
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO ai_messages (conversation_id, role, content, sources, token_usage)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at
	`, m.ConversationID, m.Role, m.Content, sourcesJSON, usageJSON).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("adding message: %w", err)
	}
	return &m, nil
}

func (s *Store) GetMessages(ctx context.Context, conversationID, userEmail string) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.conversation_id, m.role, m.content, m.sources, m.token_usage, m.created_at
		FROM ai_messages m
		JOIN ai_conversations c ON c.id = m.conversation_id
		WHERE m.conversation_id = $1 AND c.user_email = $2
		ORDER BY m.created_at ASC
	`, conversationID, userEmail)
	if err != nil {
		return nil, fmt.Errorf("getting messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		var sourcesJSON, usageJSON []byte
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &sourcesJSON, &usageJSON, &m.CreatedAt); err != nil {
			return nil, err
		}
		if len(sourcesJSON) > 0 && string(sourcesJSON) != "null" {
			_ = json.Unmarshal(sourcesJSON, &m.Sources)
		}
		if len(usageJSON) > 0 && string(usageJSON) != "null" {
			var u TokenUsage
			if err := json.Unmarshal(usageJSON, &u); err == nil {
				m.TokenUsage = &u
			}
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *Store) CountMessages(ctx context.Context, conversationID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ai_messages WHERE conversation_id = $1`, conversationID).Scan(&count)
	return count, err
}

func (s *Store) DeleteMessagesBefore(ctx context.Context, conversationID string, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM ai_messages WHERE conversation_id = $1 AND created_at < $2
	`, conversationID, before)
	if err != nil {
		return 0, fmt.Errorf("deleting messages: %w", err)
	}
	return tag.RowsAffected(), nil
}
