package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// The providers each spell an attached picture differently. These tests pin the
// four wire shapes — a wrong prefix or field name is a request the vendor
// answers with a complaint about the model, which reads like a capability
// problem and sends a person looking in the wrong place.

func testImage() types.Image {
	return types.Image{MediaType: "image/png", Data: []byte("PNG!"), Name: "shot.png"}
}

// TestOpenAICompatibleCarriesAnImageAsADataURL covers the shape the default
// route and every OpenAI-compatible endpoint use.
func TestOpenAICompatibleCarriesAnImageAsADataURL(t *testing.T) {
	msg := types.Message{Role: types.RoleUser, Content: "what is this?", Images: []types.Image{testImage()}}
	blocks := openAIContent(msg)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v, want a text block and an image block", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "what is this?" {
		t.Errorf("the text block was not kept first: %v", blocks[0])
	}
	imageBlock := blocks[1]
	if imageBlock["type"] != "image_url" {
		t.Fatalf("block type = %v, want image_url", imageBlock["type"])
	}
	url := imageBlock["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image URL = %q, want a png data URL", url)
	}
	// An empty message is still one image block: the picture is the content.
	if got := openAIContent(types.Message{Images: []types.Image{testImage()}}); len(got) != 1 {
		t.Errorf("a message of only an image produced %d blocks", len(got))
	}
}

// TestAnthropicCarriesAnImageAsABase64Source covers the block Anthropic's
// Messages API requires.
func TestAnthropicCarriesAnImageAsABase64Source(t *testing.T) {
	msg := types.Message{Role: types.RoleUser, Content: "look", Images: []types.Image{testImage()}}
	blocks := anthropicUserBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v", blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "look" {
		t.Errorf("text block = %+v", blocks[0])
	}
	img := blocks[1]
	if img.Type != "image" || img.Source == nil {
		t.Fatalf("image block = %+v", img)
	}
	if img.Source.Type != "base64" || img.Source.MediaType != "image/png" {
		t.Errorf("source = %+v", *img.Source)
	}
	if img.Source.Data != base64.StdEncoding.EncodeToString([]byte("PNG!")) {
		t.Errorf("source data = %q", img.Source.Data)
	}
	if blocks := anthropicUserBlocks(types.Message{Role: types.RoleUser}); len(blocks) != 0 {
		t.Errorf("an empty message produced %d blocks", len(blocks))
	}
}

// TestGeminiCarriesAnImageAsInlineData covers generateContent's inlineData part.
func TestGeminiCarriesAnImageAsInlineData(t *testing.T) {
	parts := geminiUserParts(types.Message{Role: types.RoleUser, Content: "look", Images: []types.Image{testImage()}})
	if len(parts) != 2 || parts[0].Text != "look" {
		t.Fatalf("parts = %+v", parts)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" {
		t.Fatalf("inline data = %+v", parts[1].InlineData)
	}
	if parts[1].InlineData.Data != base64.StdEncoding.EncodeToString([]byte("PNG!")) {
		t.Errorf("inline data = %q", parts[1].InlineData.Data)
	}
}

// TestBedrockCarriesAnImageAsAFormatAndBytes covers Converse's image block, whose
// format names the type without the image/ prefix.
func TestBedrockCarriesAnImageAsAFormatAndBytes(t *testing.T) {
	blocks := bedrockUserBlocks(types.Message{Role: types.RoleUser, Content: "look", Images: []types.Image{testImage()}})
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	if blocks[1].Image == nil {
		t.Fatalf("the image block is empty: %+v", blocks[1])
	}
	if blocks[1].Image.Format != "png" {
		t.Errorf("format = %q, want png without the image/ prefix", blocks[1].Image.Format)
	}
	if blocks[1].Image.Source.Bytes != base64.StdEncoding.EncodeToString([]byte("PNG!")) {
		t.Errorf("bytes = %q", blocks[1].Image.Source.Bytes)
	}
}

// TestOllamaCarriesImagesAsRawBase64 covers the list Ollama adds beside content.
func TestOllamaCarriesImagesAsRawBase64(t *testing.T) {
	got := ollamaImageStrings([]types.Image{testImage()})
	if len(got) != 1 || got[0] != base64.StdEncoding.EncodeToString([]byte("PNG!")) {
		t.Errorf("ollama images = %v", got)
	}
	if images := ollamaImageStrings(nil); images != nil {
		t.Errorf("a message with no images got %v", images)
	}
}

// TestARequestWithNoImagesSerializesAsBefore is the compatibility half: the
// field is omitted, so a text-only conversation's request body is byte-for-byte
// what it was.
func TestARequestWithNoImagesSerializesAsBefore(t *testing.T) {
	body, err := json.Marshal(openAIContent(types.Message{Content: "plain"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "image") {
		t.Errorf("a text-only message serialized an image: %s", body)
	}
	msg := ollamaMessage{Role: types.RoleUser, Content: "plain", Images: ollamaImageStrings(nil)}
	body, err = json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "images") {
		t.Errorf("ollama sent an images field for a text-only message: %s", body)
	}
}

// TestRunWithImagesCarriesThemIntoTheRequest is the loop-level proof: a run that
// starts with pictures puts them on the user message the provider receives, and
// keeps them in the history the next run reads.
func TestRunWithImagesCarriesThemIntoTheRequest(t *testing.T) {
	img := types.Image{MediaType: "image/png", Data: []byte("PNG!"), Name: "shot.png"}
	llm := NewMockLLM([]MockStep{{Content: "that is a green square"}})
	ag := MockAgent(llm, nil)

	if _, err := ag.RunWithImages(context.Background(), "what is this?", []types.Image{img}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(llm.Requests) != 1 {
		t.Fatalf("the provider was called %d times", len(llm.Requests))
	}
	messages := llm.Requests[0].Messages
	if len(messages) == 0 {
		t.Fatal("the request carried no messages")
	}
	last := messages[len(messages)-1]
	if last.Role != types.RoleUser || last.Content != "what is this?" {
		t.Fatalf("last message = %+v", last)
	}
	if len(last.Images) != 1 || string(last.Images[0].Data) != "PNG!" {
		t.Fatalf("the request lost the attachment: %+v", last.Images)
	}
	if len(ag.History) < 2 || len(ag.History[0].Images) != 1 {
		t.Errorf("the recorded turn lost the attachment: %+v", ag.History)
	}
}
