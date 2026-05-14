package prompt

import (
	"os"
	"sync"
	"time"
)

type Loader struct {
	path    string
	mu      sync.RWMutex
	content string
	modTime time.Time
}

func NewLoader(path string) *Loader {
	return &Loader{path: path}
}

func (l *Loader) Load() (string, error) {
	stat, err := os.Stat(l.path)
	if err != nil {
		return "", err
	}

	l.mu.RLock()
	if l.content != "" && stat.ModTime().Equal(l.modTime) {
		content := l.content
		l.mu.RUnlock()
		return content, nil
	}
	l.mu.RUnlock()

	data, err := os.ReadFile(l.path)
	if err != nil {
		return "", err
	}

	l.mu.Lock()
	l.content = string(data)
	l.modTime = stat.ModTime()
	l.mu.Unlock()

	return string(data), nil
}
