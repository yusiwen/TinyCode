package types

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngBytes returns a real PNG, encoded by the standard library, so the sniffing
// under test sees what a provider will see.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// TestReadImageSniffsTheFormatNotTheExtension is the property a provider depends
// on: the media type is what the bytes are, not what the name claims.
func TestReadImageSniffsTheFormatNotTheExtension(t *testing.T) {
	dir := t.TempDir()
	liar := filepath.Join(dir, "not-really.png")
	if err := os.WriteFile(liar, []byte("plain text pretending"), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := ReadImage(liar)
	if err == nil {
		t.Fatalf("a text file named .png was accepted as %s", img.MediaType)
	}
	if !strings.Contains(err.Error(), "text/plain") {
		t.Errorf("the refusal does not name what the file really is: %v", err)
	}

	real := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(real, pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err = ReadImage(real)
	if err != nil {
		t.Fatalf("ReadImage: %v", err)
	}
	if img.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", img.MediaType)
	}
	if img.Name != "shot.png" {
		t.Errorf("Name = %q, want the base name", img.Name)
	}
	if img.Subtype() != "png" {
		t.Errorf("Subtype = %q, want png", img.Subtype())
	}
}

// TestNewImageRefusesWhatModelsCannotRead pins the accepted set and the size cap.
func TestNewImageRefusesWhatModelsCannotRead(t *testing.T) {
	if _, err := NewImage("empty.png", nil); err == nil {
		t.Error("an empty image was accepted")
	}
	if _, err := NewImage("doc.pdf", []byte("%PDF-1.7\n")); err == nil {
		t.Error("a PDF was accepted")
	}
	if _, err := NewImage("drawing.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)); err == nil {
		t.Error("an SVG was accepted")
	}
	big := append(append([]byte{}, pngBytes(t)...), make([]byte, MaxImageBytes)...)
	_, err := NewImage("huge.png", big)
	if err == nil {
		t.Fatal("an image over the cap was accepted")
	}
	if !strings.Contains(err.Error(), "MiB") {
		t.Errorf("the size refusal does not state the sizes: %v", err)
	}
}

// TestCheckImagesBoundsAMessage covers the per-message rule the entry points use
// before they run anything.
func TestCheckImagesBoundsAMessage(t *testing.T) {
	data := pngBytes(t)
	if err := CheckImages([]Image{{MediaType: "image/png", Data: data}}); err != nil {
		t.Errorf("one valid image was refused: %v", err)
	}
	many := make([]Image, MaxImagesPerMessage+1)
	for i := range many {
		many[i] = Image{MediaType: "image/png", Data: data, Name: "x.png"}
	}
	if err := CheckImages(many); err == nil {
		t.Error("more images than the cap were accepted")
	}
	if err := CheckImages([]Image{{MediaType: "image/png"}}); err == nil {
		t.Error("an image with no bytes was accepted")
	}
	if err := CheckImages([]Image{{MediaType: "image/svg+xml", Data: data}}); err == nil {
		t.Error("an unsupported media type was accepted")
	}
}

// TestImageSurvivesTheSessionEncoding is what makes a resumed conversation keep
// its pictures: the bytes round-trip through the same JSON the session file uses.
func TestImageSurvivesTheSessionEncoding(t *testing.T) {
	data := pngBytes(t)
	msg := Message{Role: RoleUser, Content: "what is wrong with this?", Images: []Image{{
		MediaType: "image/png",
		Data:      data,
		Name:      "shot.png",
	}}}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(data)) {
		t.Error("the image is not base64 in the JSON, so the session file did not round-trip it")
	}
	var back Message
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Images) != 1 || !bytes.Equal(back.Images[0].Data, data) {
		t.Errorf("the image did not survive the round trip: %+v", back.Images)
	}
}

// TestImageWireForms pins the three spellings the providers need, because a
// wrong prefix is a request the vendor rejects with a complaint about the model.
func TestImageWireForms(t *testing.T) {
	img := Image{MediaType: "image/png", Data: []byte("abc")}
	if got, want := img.DataURL(), "data:image/png;base64,YWJj"; got != want {
		t.Errorf("DataURL = %q, want %q", got, want)
	}
	if got, want := img.Base64(), "YWJj"; got != want {
		t.Errorf("Base64 = %q, want %q", got, want)
	}
	if got, want := img.Label(), "image/png"; got != want {
		t.Errorf("Label with no name = %q, want %q", got, want)
	}
}
