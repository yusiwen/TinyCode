package skill

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// Skill holds metadata and content for one discovered skill.
type Skill struct {
	Name        string
	Description string
	Content     string // full SKILL.md content (including frontmatter)
	Builtin     bool   // true if compiled into binary
	Path        string // file path (for user skills) or "builtin"
}

// Discover scans all skill sources and returns the merged list.
// Order: builtin → global (~/.tinycode/skills/) → project (.tinycode/skills/ from cwd upward).
// Later sources override earlier ones with the same name.
func Discover(cwd string) []Skill {
	var skills []Skill

	// 1. Builtin (embedded)
	entries, err := builtinFS.ReadDir("builtin")
	if err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			if s := parseFile(e.Name(), readBuiltin(e.Name()), "builtin", true); s != nil {
				skills = append(skills, *s)
			}
		}
	}

	// 2. Global (~/.tinycode/skills/)
	home, _ := os.UserHomeDir()
	if home != "" {
		globalDir := filepath.Join(home, ".tinycode", "skills")
		skills = append(skills, scanDir(globalDir, false)...)
	}

	// 3. Project (.tinycode/skills/ from cwd upward)
	if cwd != "" {
		projectDir := findProjectDir(cwd, ".tinycode", "skills")
		if projectDir != "" {
			skills = append(skills, scanDir(projectDir, false)...)
		}
	}

	// Deduplicate: later sources override earlier ones with the same name
	seen := map[string]bool{}
	var deduped []Skill
	for i := len(skills) - 1; i >= 0; i-- {
		s := skills[i]
		if !seen[s.Name] {
			deduped = append([]Skill{s}, deduped...)
			seen[s.Name] = true
		}
	}

	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Builtin != deduped[j].Builtin {
			return deduped[i].Builtin // builtin first (if no override exists)
		}
		return deduped[i].Name < deduped[j].Name
	})

	return deduped
}

// DiscoveredNames returns "name — description" lines for system prompt injection.
func DiscoveredNames(cwd string) string {
	skills := Discover(cwd)
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nAvailable skills:\n")
	for _, s := range skills {
		b.WriteString("- " + s.Name + ": " + s.Description + "\n")
	}
	b.WriteString("Use the load_skill tool to load a skill's instructions.")
	return b.String()
}

// FindByName locates a skill by name across all sources.
func FindByName(name string, cwd string) *Skill {
	for _, s := range Discover(cwd) {
		if strings.EqualFold(s.Name, name) {
			return &s
		}
	}
	return nil
}

// LoadContent returns the full SKILL.md content for a skill.
func LoadContent(name string, cwd string) string {
	s := FindByName(name, cwd)
	if s == nil {
		return ""
	}
	if s.Builtin {
		return readBuiltin(name + ".md")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return ""
	}
	return string(data)
}

// loaded tracks which skills have been loaded via load_skill tool (cross-session dedup).
var loaded = make(map[string]bool)

// UserSkillDir returns the user's skill directory (~/.tinycode/skills/).
func UserSkillDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".tinycode", "skills")
}

// ResetOne clears the loaded cache for a single skill, allowing re-load.
func ResetOne(name string) {
	delete(loaded, strings.ToLower(name))
}

// DeleteOne removes a user skill from disk. Returns an error if the skill
// is builtin (read-only) or does not exist.
func DeleteOne(name string) error {
	name = strings.ToLower(name)
	// Don't allow deleting builtin skills
	if FindByName(name, "") != nil {
		s := FindByName(name, "")
		if s != nil && s.Builtin {
			return fmt.Errorf("cannot delete builtin skill: %s", name)
		}
	}
	dir := filepath.Join(UserSkillDir(), name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill not found: %s", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	delete(loaded, name)
	return nil
}

// CreateOne creates a new user skill from a SKILL.md content string.
// Returns the name of the created skill.
func CreateOne(content string) (string, error) {
	// Parse the frontmatter to extract the name
	s := parseFile("", content, "", false)
	if s == nil || s.Name == "" {
		return "", fmt.Errorf("invalid SKILL.md: missing name in frontmatter")
	}
	name := strings.ToLower(s.Name)
	dir := filepath.Join(UserSkillDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write %s/SKILL.md: %w", name, err)
	}
	// Clear cache so next LoadOnce re-reads
	ResetOne(name)
	return name, nil
}

// EditOne updates an existing user skill. For builtin skills, creates a user override.
func EditOne(name string, content string) (isOverride bool, err error) {
	normalized := strings.ToLower(name)
	// Check if existing is builtin
	if FindByName(normalized, "") != nil {
		if s := FindByName(normalized, ""); s != nil && s.Builtin {
			// Create user override (don't touch the embedded file)
			isOverride = true
		}
	}
	dir := filepath.Join(UserSkillDir(), normalized)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return isOverride, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		return isOverride, fmt.Errorf("write %s/SKILL.md: %w", normalized, err)
	}
	ResetOne(normalized)
	return isOverride, nil
}

// ListAll returns all skills with their source type.
func ListAll(cwd string) []SkillWithSource {
	skills := Discover(cwd)
	result := make([]SkillWithSource, len(skills))
	for i, s := range skills {
		src := "user"
		if s.Builtin {
			src = "builtin"
		}
		result[i] = SkillWithSource{Skill: s, Source: src}
	}
	return result
}

// SkillWithSource pairs a Skill with its source label.
type SkillWithSource struct {
	Skill  Skill
	Source string // "builtin" or "user"
}

// LoadOnce returns the full SKILL.md content for a skill, but only the first time.
// The bool return indicates whether this was a fresh load (true) or a repeat (false).
// Use as the Execute body of the load_skill tool.
func LoadOnce(name, cwd string) (string, bool) {
	name = strings.ToLower(name)
	if loaded[name] {
		return "", false
	}
	content := LoadContent(name, cwd)
	if content == "" {
		return "", false
	}
	loaded[name] = true
	return content, true
}

// ResetLoaded clears the loaded-skills cache (used in tests).
func ResetLoaded() {
	loaded = make(map[string]bool)
}

// --- internal helpers ---

func readBuiltin(name string) string {
	data, err := builtinFS.ReadFile("builtin/" + name)
	if err != nil {
		return ""
	}
	return string(data)
}

func scanDir(dir string, builtin bool) []Skill {
	var skills []Skill
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mdPath := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(mdPath)
		if err != nil {
			continue
		}
		s := parseFile(e.Name(), string(data), mdPath, false)
		if s != nil {
			skills = append(skills, *s)
		}
	}
	return skills
}

func parseFile(filename, content, path string, builtin bool) *Skill {
	// Extract name from first SKILL.md style frontmatter
	name := ""
	desc := ""

	// Try YAML frontmatter: ---\nname: ...\ndescription: ...\n---
	rest := content
	if strings.HasPrefix(strings.TrimSpace(rest), "---") {
		parts := strings.SplitN(rest[3:], "---", 2)
		if len(parts) == 2 {
			front := strings.TrimSpace(parts[0])
			for _, line := range strings.Split(front, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(line[5:])
				}
				if strings.HasPrefix(line, "description:") {
					desc = strings.TrimSpace(line[12:])
				}
			}
		}
	}

	// Fallback: derive name from filename (e.g., "code-review.md" → "code-review")
	if name == "" {
		name = strings.TrimSuffix(filename, ".md")
		// If it's in a subdirectory, use the dir name
	}
	if name == "" {
		return nil
	}
	if desc == "" {
		desc = name // fallback
	}

	return &Skill{
		Name:        name,
		Description: desc,
		Content:     content,
		Builtin:     builtin,
		Path:        path,
	}
}

// findProjectDir walks upward from start looking for a subdirectory like ".tinycode/skills".
func findProjectDir(start, subdir, skillDir string) string {
	dir := start
	for {
		candidate := filepath.Join(dir, subdir, skillDir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
