package feishu

import "sync"

type reactionStateStore struct {
	mu    sync.Mutex
	items map[string]string
}

func newReactionStateStore() *reactionStateStore {
	return &reactionStateStore{items: make(map[string]string)}
}

func (s *reactionStateStore) Get(messageID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[messageID]
}

func (s *reactionStateStore) Set(messageID string, reactionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reactionID == "" {
		delete(s.items, messageID)
		return
	}
	s.items[messageID] = reactionID
}
