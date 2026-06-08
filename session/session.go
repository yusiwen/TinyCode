package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// Session stores conversation history.
type Session struct {
	mu           sync.Mutex
	ID           string          `json:"id"`
	Title        string          `json:"title,omitempty"`
	Preview      string          `json:"preview,omitempty"`
	ModelName    string          `json:"model_name,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	MessageCount int             `json:"message_count"`
	Messages     []types.Message `json:"messages"`
	dir          string
}

// New creates a new session.
func New(id, dir string) *Session {
	os.MkdirAll(dir, 0755)
	return &Session{ID: id, dir: dir, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

// Append adds a message to the session.
func (s *Session) Append(msg types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
	return nil
}

// Flush persists to disk, deriving metadata from messages.
func (s *Session) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.UpdatedAt = time.Now()
	s.MessageCount = len(s.Messages)

	// Derive title from first user message
	s.Title = ""
	for _, m := range s.Messages {
		if m.Role == "user" && m.Content != "" {
			s.Title = truncate(m.Content, 80)
			break
		}
	}

	// Derive preview from last assistant content
	s.Preview = ""
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role == "assistant" && m.Content != "" {
			s.Preview = truncate(m.Content, 120)
			break
		}
	}

	path := filepath.Join(s.dir, s.ID+".json")
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
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

// SessionInfo summarizes a session for display purposes.
type SessionInfo struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Preview      string    `json:"preview,omitempty"`
	ModelName    string    `json:"model_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// List returns all available session infos sorted by update time (newest last).
func (st *Store) List() []SessionInfo {
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		return nil
	}
	var infos []SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(id, st.Dir)
		if err != nil {
			continue
		}
		infos = append(infos, SessionInfo{
			ID:           id,
			Title:        s.Title,
			Preview:      s.Preview,
			ModelName:    s.ModelName,
			CreatedAt:    s.CreatedAt,
			UpdatedAt:    s.UpdatedAt,
			MessageCount: s.MessageCount,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].UpdatedAt.Before(infos[j].UpdatedAt)
	})
	return infos
}
