package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/types"
)

// writeTestPNG writes a real 1x1 PNG, so the attach path sniffs what a provider
// would see rather than a fixture that only looks like one.
func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

// TestImageCommandAttachesListsAndClears is the command's whole contract in one
// place: a path attaches, a bare /image lists, and clear drops the list.
func TestImageCommandAttachesListsAndClears(t *testing.T) {
	dir := t.TempDir()
	path := writeTestPNG(t, dir, "shot.png")
	m := frameModel(80, 24)

	m.handleImage("/image " + path)
	if len(m.pendingImages) != 1 {
		t.Fatalf("pending = %d, want 1", len(m.pendingImages))
	}
	if got := m.pendingImages[0]; got.MediaType != "image/png" || got.Name != "shot.png" {
		t.Errorf("attached %+v", got)
	}
	if last := m.messages[len(m.messages)-1].Content; !strings.Contains(last, "shot.png") {
		t.Errorf("the transcript does not name the attachment: %q", last)
	}

	m.handleImage("/image")
	if last := m.messages[len(m.messages)-1].Content; !strings.Contains(last, "shot.png") {
		t.Errorf("the listing does not name the attachment: %q", last)
	}

	m.handleImage("/image clear")
	if len(m.pendingImages) != 0 {
		t.Errorf("clear left %d images", len(m.pendingImages))
	}
	if last := m.messages[len(m.messages)-1].Content; !strings.Contains(last, "Cleared") {
		t.Errorf("clear did not say so: %q", last)
	}
}

// TestImageCommandRefusesWhatIsNotAnImage keeps a refusal in the transcript
// instead of a pending attachment the model would never receive.
func TestImageCommandRefusesWhatIsNotAnImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("not a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := frameModel(80, 24)
	m.handleImage("/image " + path)
	if len(m.pendingImages) != 0 {
		t.Fatalf("a text file was attached: %+v", m.pendingImages)
	}
	if last := m.messages[len(m.messages)-1].Content; !strings.Contains(last, "Cannot attach") {
		t.Errorf("the refusal was not recorded: %q", last)
	}
}

// TestImageCommandHonoursThePerMessageCap stops a fifth attachment at the same
// limit the request path enforces.
func TestImageCommandHonoursThePerMessageCap(t *testing.T) {
	dir := t.TempDir()
	m := frameModel(80, 24)
	for i := 0; i < types.MaxImagesPerMessage; i++ {
		m.handleImage("/image " + writeTestPNG(t, dir, strings.Repeat("a", i+1)+".png"))
	}
	if len(m.pendingImages) != types.MaxImagesPerMessage {
		t.Fatalf("pending = %d, want %d", len(m.pendingImages), types.MaxImagesPerMessage)
	}
	m.handleImage("/image " + writeTestPNG(t, dir, "extra.png"))
	if len(m.pendingImages) != types.MaxImagesPerMessage {
		t.Errorf("the cap did not hold: %d pending", len(m.pendingImages))
	}
	if last := m.messages[len(m.messages)-1].Content; !strings.Contains(last, "At most") {
		t.Errorf("the cap refusal does not explain itself: %q", last)
	}
}

// TestTheNextRunTakesTheAttachments is what "next message" means: the run that
// starts collects the list and empties it, so the following message does not
// silently repeat the pictures.
func TestTheNextRunTakesTheAttachments(t *testing.T) {
	dir := t.TempDir()
	m := frameModel(80, 24)
	m.handleImage("/image " + writeTestPNG(t, dir, "one.png"))

	images := m.takePendingImages()
	if len(images) != 1 {
		t.Fatalf("the run took %d images, want 1", len(images))
	}
	if len(m.pendingImages) != 0 {
		t.Error("the pending list was not emptied")
	}
	if again := m.takePendingImages(); again != nil {
		t.Errorf("a second run took %d images", len(again))
	}
}

// TestAPictureSurvivesTheSessionFile is the resume contract: the bytes are in the
// session file, and loading it back gives the message its picture again.
func TestAPictureSurvivesTheSessionFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTestPNG(t, dir, "persisted.png")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	m := frameModel(80, 24)
	m.agent = &agent.Agent{}
	m.SessionDir = t.TempDir()
	m.currentBranch = "TUI-20260101-000000-weighty-otter"
	m.messages = append(m.messages, chatMessage{
		Role:    "user",
		Content: "what is wrong here?",
		Images:  []types.Image{{MediaType: "image/png", Data: data, Name: "persisted.png"}},
	})
	m.persistSession()

	stored, err := session.Load(m.currentBranch, m.SessionDir)
	if err != nil {
		t.Fatalf("load the session back: %v", err)
	}
	var found bool
	for _, msg := range stored.Messages {
		if msg.Role == types.RoleUser && len(msg.Images) == 1 {
			found = true
			if !bytes.Equal(msg.Images[0].Data, data) {
				t.Error("the stored image bytes differ from the attached ones")
			}
			if msg.Images[0].MediaType != "image/png" {
				t.Errorf("stored media type = %q", msg.Images[0].MediaType)
			}
		}
	}
	if !found {
		t.Fatalf("no user message in the session file carries the image: %+v", stored.Messages)
	}

	// And the restored transcript renders the attachment with its message.
	m.adoptSession(stored)
	var rendered bool
	for _, msg := range m.messages {
		for _, chunk := range (UserComponent{}).Render(msg, false) {
			if strings.Contains(chunk.Text, "persisted.png") {
				rendered = true
			}
		}
	}
	if !rendered {
		t.Error("the restored transcript does not show the attachment")
	}
}
