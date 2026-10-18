package task

import (
	"encoding/json"
	"fmt"
)

// The wire format for one audit record is {"type": <name>, "level": <level>},
// on every path.

// Event is one audit record.
type Event struct {
	Name  string
	Level int
}

// Encode renders the record through a struct.
func (e Event) Encode() (string, error) {
	type wire struct {
		Name  string `json:"type"`
		Level int    `json:"level"`
	}
	data, err := json.Marshal(wire{Name: e.Name, Level: e.Level})
	return string(data), err
}

// EncodeMap renders the same record as a map.
func (e Event) EncodeMap() map[string]any {
	return map[string]any{"type": e.Name, "level": e.Level}
}

// Decode reads a record in the same shape.
func Decode(data string) (Event, error) {
	var wire struct {
		Name  string `json:"type"`
		Level int    `json:"level"`
	}
	if err := json.Unmarshal([]byte(data), &wire); err != nil {
		return Event{}, err
	}
	if wire.Name == "" {
		return Event{}, fmt.Errorf("decode: record has no type")
	}
	return Event{Name: wire.Name, Level: wire.Level}, nil
}
