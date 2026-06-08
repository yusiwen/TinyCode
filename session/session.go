package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/yusiwen/tinycode/types"
)

// Session stores conversation history.
type Session struct {
	mu       sync.Mutex
	ID       string          `json:"id"`
	Messages []types.Message `json:"messages"`
	dir      string
}

// New creates a new session.
func New(id, dir string) *Session {
	os.MkdirAll(dir, 0755)
	return &Session{ID: id, dir: dir}
}

// Append adds a message to the session.
func (s *Session) Append(msg types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	return nil
}

// Flush persists to disk.
func (s *Session) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, s.ID+".json")
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Load reads a session from disk.
func Load(id, dir string) (*Session, error) {
	path := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, dir: dir}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Store manages multiple sessions.
type Store struct {
	Dir string
}

func NewStore(dir string) *Store {
	os.MkdirAll(dir, 0755)
	return &Store{Dir: dir}
}

func (st *Store) Create(id string) *Session {
	return New(id, st.Dir)
}

func (st *Store) Load(id string) (*Session, error) {
	return Load(id, st.Dir)
}

// List returns all available session IDs sorted by name (most recent last).
func (st *Store) List() []string {
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(ids)
	return ids
}
