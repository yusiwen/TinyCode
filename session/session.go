package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// Session stores conversation history.
type Session struct {
	mu           sync.Mutex
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Preview      string    `json:"preview,omitempty"`
	ModelName    string    `json:"model_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`

	// Branch tree support
	ParentSessionID string `json:"parent_session,omitempty"`
	ForkAt          int    `json:"fork_at,omitempty"` // message index where fork happened

	// Permission paths allowed for this session (Allow session)
	AllowedPaths []string `json:"allowed_paths,omitempty"`

	Messages []types.Message `json:"messages"`
	dir      string
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

	path, err := pathFor(s.dir, s.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// fsyncs it and renames it into place. Sessions contain the full conversation,
// so a crash mid-write must not leave a truncated JSON file behind, and the file
// is created with mode 0600.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp session file: %w", err)
	}
	tmpName := tmp.Name()
	// Remove the temp file unless the rename below consumed it.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp session file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp session file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp session file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// validIDRe restricts session ids and fork labels to a path-safe charset. Ids
// become file names, so separators, ".." and absolute paths must be rejected.
var validIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidateID reports whether id is a legal session id or branch label.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("session id is required")
	}
	if !validIDRe.MatchString(id) {
		return fmt.Errorf("invalid session id %q: use 1-128 chars of [A-Za-z0-9._-], starting with a letter or digit", id)
	}
	return nil
}

// pathFor returns the JSON file for id and verifies it stays inside dir.
func pathFor(dir, id string) (string, error) {
	if err := ValidateID(id); err != nil {
		return "", err
	}
	if dir == "" {
		return "", fmt.Errorf("session directory is not set")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve session dir: %w", err)
	}
	p := filepath.Join(absDir, id+".json")
	rel, err := filepath.Rel(absDir, p)
	if err != nil {
		return "", fmt.Errorf("resolve session path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("session id %q escapes %s", id, absDir)
	}
	return p, nil
}

// Load reads a session from disk.
func Load(id, dir string) (*Session, error) {
	path, err := pathFor(dir, id)
	if err != nil {
		return nil, err
	}
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

// Delete removes a session from disk.
func (st *Store) Delete(id string) error {
	path, err := pathFor(st.Dir, id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Fork creates and persists a new branch session.
// parentID is the source session. Messages up to forkAt (exclusive) are copied.
func (st *Store) Fork(parentID string, forkAt int, label string) (*Session, error) {
	// The label comes from user input (/fork <label>) and becomes part of the
	// branch file name, so it must be path-safe.
	if label != "" {
		if err := ValidateID(label); err != nil {
			return nil, fmt.Errorf("invalid fork label: %w", err)
		}
	}
	parent, err := st.Load(parentID)
	if err != nil {
		return nil, fmt.Errorf("load parent: %w", err)
	}

	// Build branch ID
	branchID := parentID + "-" + label
	if label != "" {
		// Refuse to clobber an existing branch instead of overwriting it.
		if existing, err := pathFor(st.Dir, branchID); err == nil {
			if _, statErr := os.Stat(existing); statErr == nil {
				return nil, fmt.Errorf("branch %q already exists", branchID)
			}
		}
	}
	if label == "" {
		entries, err := os.ReadDir(st.Dir)
		if err != nil {
			return nil, fmt.Errorf("read session dir: %w", err)
		}
		used := make(map[string]bool)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				used[strings.TrimSuffix(e.Name(), ".json")] = true
			}
		}
		n := 1
		for {
			branchID = fmt.Sprintf("%s-branch-%d", parentID, n)
			if !used[branchID] {
				break
			}
			n++
		}
	}

	// Copy shared messages up to forkAt. Callers pass an in-memory message
	// count that can exceed what was persisted, so clamp instead of panicking.
	if forkAt < 0 {
		forkAt = 0
	}
	if forkAt > len(parent.Messages) {
		forkAt = len(parent.Messages)
	}
	shared := make([]types.Message, forkAt)
	copy(shared, parent.Messages[:forkAt])

	branch := &Session{
		ID:              branchID,
		ParentSessionID: parentID,
		ForkAt:          forkAt,
		Messages:        shared,
		dir:             st.Dir,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
		Title:           parent.Title,
		ModelName:       parent.ModelName,
		MessageCount:    forkAt,
	}

	// Persist immediately so the file exists for subsequent forks
	if err := branch.Flush(); err != nil {
		return nil, fmt.Errorf("flush branch: %w", err)
	}
	return branch, nil
}

// Search returns sessions whose content matches the query string.
func (st *Store) Search(query string) []SessionInfo {
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		return nil
	}
	q := strings.ToLower(query)
	var results []SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(id, st.Dir)
		if err != nil {
			continue
		}
		matched := strings.Contains(strings.ToLower(s.Title), q) ||
			strings.Contains(strings.ToLower(s.Preview), q)
		if !matched {
			for _, m := range s.Messages {
				if strings.Contains(strings.ToLower(m.Content), q) ||
					strings.Contains(strings.ToLower(m.ReasoningContent), q) {
					matched = true
					break
				}
			}
		}
		if matched {
			results = append(results, SessionInfo{
				ID:           id,
				Title:        s.Title,
				Preview:      s.Preview,
				ModelName:    s.ModelName,
				CreatedAt:    s.CreatedAt,
				UpdatedAt:    s.UpdatedAt,
				MessageCount: s.MessageCount,
			})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.Before(results[j].UpdatedAt)
	})
	return results
}

// ExportMarkdown returns the conversation as a Markdown string.
func (s *Session) ExportMarkdown() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Session: %s\n\n", s.Title))
	b.WriteString(fmt.Sprintf("**Model:** %s  \n", s.ModelName))
	b.WriteString(fmt.Sprintf("**Started:** %s  \n", s.CreatedAt.Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("**Messages:** %d  \n\n", s.MessageCount))
	b.WriteString("---\n\n")
	for _, m := range s.Messages {
		switch m.Role {
		case "user":
			b.WriteString(fmt.Sprintf("**User:**\n%s\n\n", m.Content))
		case "assistant":
			if m.ReasoningContent != "" {
				b.WriteString(fmt.Sprintf("**Reasoning:**\n%s\n\n", m.ReasoningContent))
			}
			b.WriteString(fmt.Sprintf("**Assistant:**\n%s\n\n", m.Content))
		case "tool":
			b.WriteString(fmt.Sprintf("**Tool (%s):**\n%s\n\n", m.Name, truncate(m.Content, 500)))
		case "system":
			b.WriteString(fmt.Sprintf("**System:**\n%s\n\n", m.Content))
		}
	}
	return b.String()
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
