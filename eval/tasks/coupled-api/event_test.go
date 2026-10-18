package task

import (
	"encoding/json"
	"reflect"
	"testing"
)

const record = `{"type":"login","level":2}`

// sameJSON reports whether two JSON objects carry the same fields. Key order is
// not part of the format, so comparing the encoded strings would test the
// encoder's field order instead of the protocol.
func sameJSON(t *testing.T, got string, want string) bool {
	t.Helper()
	var gotObject, wantObject map[string]any
	if err := json.Unmarshal([]byte(got), &gotObject); err != nil {
		t.Fatalf("%q is not a JSON object: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantObject); err != nil {
		t.Fatalf("%q is not a JSON object: %v", want, err)
	}
	return reflect.DeepEqual(gotObject, wantObject)
}

func TestEveryEncoderAgreesOnTheWireFormat(t *testing.T) {
	event := Event{Name: "login", Level: 2}

	encoded, err := event.Encode()
	if err != nil {
		t.Fatalf("Encode = %v", err)
	}
	if !sameJSON(t, encoded, record) {
		t.Errorf("Encode = %s, want %s", encoded, record)
	}

	fromMap, err := json.Marshal(event.EncodeMap())
	if err != nil {
		t.Fatalf("EncodeMap does not marshal: %v", err)
	}
	if !sameJSON(t, string(fromMap), record) {
		t.Errorf("EncodeMap = %s, want %s", fromMap, record)
	}
}

func TestDecodeReadsTheSameShape(t *testing.T) {
	event, err := Decode(record)
	if err != nil {
		t.Fatalf("Decode = %v", err)
	}
	if event.Name != "login" || event.Level != 2 {
		t.Errorf("Decode = %+v, want {login 2}", event)
	}
	if _, err := Decode(`{"name":"login","level":2}`); err == nil {
		t.Error("Decode accepted a record with no type field")
	}
}
