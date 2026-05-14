package session

import (
	"context"
	"sync"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Session struct {
	ID            string
	ChatID        string
	ThreadID      string
	RootMessageID string
	CreatedBy     string
	Summary       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Message struct {
	ID              string
	SessionID       string
	FeishuMessageID string
	Role            Role
	Content         string
	ImageURLs       []string
	CreatedAt       time.Time
}

type Store interface {
	GetOrCreate(ctx context.Context, key SessionKey) (*Session, error)
	AddMessage(ctx context.Context, msg Message) error
	RecentMessages(ctx context.Context, sessionID string, limit int) ([]Message, error)
}

type SessionKey struct {
	ChatID        string
	ThreadID      string
	RootMessageID string
	CreatedBy     string
}

type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	messages map[string][]Message
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*Session),
		messages: make(map[string][]Message),
	}
}

func (s *MemoryStore) GetOrCreate(_ context.Context, key SessionKey) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := key.ChatID + ":" + key.ThreadID
	if existing, ok := s.sessions[id]; ok {
		existing.UpdatedAt = time.Now()
		return cloneSession(existing), nil
	}

	now := time.Now()
	created := &Session{
		ID:            id,
		ChatID:        key.ChatID,
		ThreadID:      key.ThreadID,
		RootMessageID: key.RootMessageID,
		CreatedBy:     key.CreatedBy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.sessions[id] = created
	return cloneSession(created), nil
}

func (s *MemoryStore) AddMessage(_ context.Context, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	s.messages[msg.SessionID] = append(s.messages[msg.SessionID], msg)
	if sess, ok := s.sessions[msg.SessionID]; ok {
		sess.UpdatedAt = time.Now()
	}
	return nil
}

func (s *MemoryStore) RecentMessages(_ context.Context, sessionID string, limit int) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := s.messages[sessionID]
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	out := make([]Message, len(all))
	copy(out, all)
	return out, nil
}

func cloneSession(in *Session) *Session {
	out := *in
	return &out
}
