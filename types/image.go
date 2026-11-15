package types

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// An image is a picture the person attached to a message: a screenshot, a
// design, a photograph of an error. It travels with the user message, is stored
// with it in the session file, and is serialized by each provider backend as
// that vendor's content block — this file owns only the part that is the same
// everywhere: what is accepted, and how big it may be.

// MaxImageBytes bounds one image. The cap is on the *file* — the price is the
// request body and the model's context, so it is the bytes the model will read,
// not the pixels on screen.
const MaxImageBytes = 4 << 20

// MaxImagesPerMessage bounds one attachment. Beyond a handful, the model's
// context is spent on pictures rather than on the task, and the request body
// grows to a size providers start refusing.
const MaxImagesPerMessage = 4

// imageMediaTypes are the formats the providers on this machine accept. SVG is
// absent on purpose: it is a document, not a raster image, and the model
// inspects a rasterized rendering or not at all; PDF is a document too.
var imageMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// IsImageMediaType reports whether a sniffed media type is one a model accepts.
// It is what the readers and the refusal paths share, so "accepted" is one list.
func IsImageMediaType(mediaType string) bool { return imageMediaTypes[mediaType] }

// Image is one picture attached to a user message.
type Image struct {
	// MediaType is the sniffed type ("image/png"), not the file extension: an
	// extension is a claim, and the providers want the truth.
	MediaType string `json:"media_type"`
	// Data is the picture's bytes. JSON carries them base64-encoded, which is
	// what every provider wire format wants anyway.
	Data []byte `json:"data"`
	// Name is the file name, kept so the transcript can say what was attached
	// without keeping a second copy of the path.
	Name string `json:"name,omitempty"`
}

// ReadImage loads one image from disk and checks that it is something a model
// accepts. The type is sniffed from the bytes, so a .png that is really a PDF is
// refused as a PDF.
func ReadImage(path string) (Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Image{}, fmt.Errorf("read image %s: %w", path, err)
	}
	return NewImage(filepath.Base(path), data)
}

// NewImage validates bytes that did not come from a file (a clipboard, a test)
// and builds the attachment.
func NewImage(name string, data []byte) (Image, error) {
	if len(data) == 0 {
		return Image{}, fmt.Errorf("image %s is empty", name)
	}
	if len(data) > MaxImageBytes {
		return Image{}, fmt.Errorf("image %s is %s, over the %s limit",
			name, humanBytes(len(data)), humanBytes(MaxImageBytes))
	}
	mediaType := http.DetectContentType(data)
	if !IsImageMediaType(mediaType) {
		return Image{}, fmt.Errorf("image %s is %s; attach a png, jpeg, gif or webp",
			name, mediaType)
	}
	return Image{MediaType: mediaType, Data: data, Name: name}, nil
}

// CheckImages bounds a message's attachments.
func CheckImages(images []Image) error {
	if len(images) > MaxImagesPerMessage {
		return fmt.Errorf("%d images attached; at most %d per message", len(images), MaxImagesPerMessage)
	}
	for _, img := range images {
		if img.MediaType == "" || !imageMediaTypes[img.MediaType] {
			return fmt.Errorf("attached image %s is %q; attach a png, jpeg, gif or webp", img.Name, img.MediaType)
		}
		if len(img.Data) == 0 {
			return fmt.Errorf("attached image %s is empty", img.Name)
		}
		if len(img.Data) > MaxImageBytes {
			return fmt.Errorf("attached image %s is %s, over the %s limit",
				img.Name, humanBytes(len(img.Data)), humanBytes(MaxImageBytes))
		}
	}
	return nil
}

// DataURL is the data: URL the OpenAI-compatible shape wants.
func (i Image) DataURL() string {
	return "data:" + i.MediaType + ";base64," + base64.StdEncoding.EncodeToString(i.Data)
}

// Base64 is the raw base64 the vendors that take it separately want (Anthropic,
// Gemini, Ollama).
func (i Image) Base64() string { return base64.StdEncoding.EncodeToString(i.Data) }

// Label is what the transcript and a refusal call this image: the name it was
// attached under, without a path.
func (i Image) Label() string {
	if i.Name != "" {
		return i.Name
	}
	return i.MediaType
}

// Subtype is the format name without the "image/" prefix: what Bedrock and
// Anthropic's media-type field want spelled separately.
func (i Image) Subtype() string { return strings.TrimPrefix(i.MediaType, "image/") }

// humanBytes renders a byte count the way a refusal should say it.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
