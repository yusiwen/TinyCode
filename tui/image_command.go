package tui

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/i18n"
	"github.com/yusiwen/tinycode/internal/home"
	"github.com/yusiwen/tinycode/types"
)

// /image attaches a picture to the next message. The picture is loaded here,
// not by the agent, because attaching is the person's action: the file is opened
// with their own permissions, before any run exists, and the result is held until
// a message can carry it.
//
// The pending list is what "next message" means. A run that starts while images
// are pending takes them with it and clears the list; `/image clear` drops them.

// takePendingImages hands the attachments to the run that is starting.
func (m *TuiModel) takePendingImages() []types.Image {
	if len(m.pendingImages) == 0 {
		return nil
	}
	images := m.pendingImages
	m.pendingImages = nil
	return images
}

// handleImage implements `/image <path>`, `/image` and `/image clear`.
func (m *TuiModel) handleImage(cmd string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(cmd, "/image"))
	switch arg {
	case "":
		if len(m.pendingImages) == 0 {
			m.appendSystem(i18n.T("No images attached. Use /image <path> to attach one."))
			return m, nil
		}
		var lines []string
		for _, img := range m.pendingImages {
			lines = append(lines, "  "+img.Label()+" ("+img.MediaType+")")
		}
		m.appendSystem(i18n.T("Attached to your next message:\n%s", strings.Join(lines, "\n")))
		return m, nil
	case "clear":
		if len(m.pendingImages) == 0 {
			m.appendSystem(i18n.T("No images attached."))
			return m, nil
		}
		m.pendingImages = nil
		m.appendSystem(i18n.T("Cleared the attached images."))
		return m, nil
	}

	paths := splitPaths(arg)
	if len(m.pendingImages)+len(paths) > types.MaxImagesPerMessage {
		m.appendSystem(i18n.T("At most %d images can go with one message; %d are already attached.",
			types.MaxImagesPerMessage, len(m.pendingImages)))
		return m, nil
	}
	var attached []string
	for _, path := range paths {
		img, err := types.ReadImage(expandUserPath(path))
		if err != nil {
			m.appendSystem(i18n.T("Cannot attach %s: %v", path, err))
			continue
		}
		m.pendingImages = append(m.pendingImages, img)
		attached = append(attached, img.Label())
	}
	if len(attached) > 0 {
		m.appendSystem(i18n.T("Attached %s. It goes with your next message.", strings.Join(attached, ", ")))
	}
	return m, nil
}

// splitPaths accepts one path or several separated by spaces or commas. A path
// containing a space has to be quoted; `/image` splits on whitespace first, so
// the quoted form is joined back here.
func splitPaths(arg string) []string {
	arg = strings.NewReplacer(",", " ").Replace(arg)
	var out []string
	var cur strings.Builder
	inQuote := rune(0)
	for _, r := range arg {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			inQuote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// expandUserPath expands a leading ~ the way a shell would, so a person can type
// the path they see in their file manager.
func expandUserPath(path string) string {
	if path == "~" {
		if home, err := home.Dir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := home.Dir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
