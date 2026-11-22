package task

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("%s is not a JSON object: %v", raw, err)
	}
	return object
}

func due() *time.Time {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	return &at
}

func TestEncodersAgree(t *testing.T) {
	tasks := map[string]Task{
		"full": {
			ID: "t1", Title: "ship it", Due: due(),
			Tags: []string{"a", "b"}, Priority: 3,
		},
		"empty": {},
		"title only": {Title: "x"},
		"tags empty slice": {ID: "t2", Tags: []string{}},
		"priority zero":    {ID: "t3", Priority: 0},
		"due only":         {Due: due()},
	}
	for name, task := range tasks {
		t.Run(name, func(t *testing.T) {
			direct, err := task.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON = %v", err)
			}
			viaMap, err := json.Marshal(task.Map())
			if err != nil {
				t.Fatalf("Map does not marshal: %v", err)
			}
			if !reflect.DeepEqual(decode(t, direct), decode(t, viaMap)) {
				t.Errorf("MarshalJSON = %s\n     Map+json = %s", direct, viaMap)
			}
		})
	}
}

func TestWireFormat(t *testing.T) {
	full := Task{ID: "t1", Title: "ship it", Due: due(), Tags: []string{"a", "b"}, Priority: 3}
	raw, err := full.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON = %v", err)
	}
	want := map[string]any{
		"id": "t1", "title": "ship it", "due": "2026-03-04T05:06:07Z",
		"tags": []any{"a", "b"}, "priority": float64(3),
	}
	if got := decode(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("MarshalJSON = %s, want %v", raw, want)
	}
}

func TestOmissions(t *testing.T) {
	cases := map[string]struct {
		task Task
		want []string
	}{
		"empty task has no fields":       {Task{}, nil},
		"zero priority is omitted":       {Task{ID: "t", Priority: 0}, []string{"id"}},
		"empty slice is omitted":         {Task{ID: "t", Tags: []string{}}, []string{"id"}},
		"empty string title is omitted":  {Task{ID: "t", Title: ""}, []string{"id"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := tc.task.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON = %v", err)
			}
			object := decode(t, raw)
			for _, key := range tc.want {
				if _, ok := object[key]; !ok {
					t.Errorf("%s must contain %q, got %v", raw, key, object)
				}
			}
			if len(object) != len(tc.want) {
				t.Errorf("%s has %d field(s), want %d (%v)", raw, len(object), len(tc.want), tc.want)
			}
		})
	}
}

func TestDueIsUTCWithSecondPrecision(t *testing.T) {
	inTokyo := time.Date(2026, 3, 4, 14, 6, 7, 123456789, time.FixedZone("JST", 9*3600))
	task := Task{Due: &inTokyo}
	raw, err := task.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON = %v", err)
	}
	object := decode(t, raw)
	if got, want := object["due"], "2026-03-04T05:06:07Z"; got != want {
		t.Errorf("due = %v, want %v (UTC, seconds)", got, want)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	raw, err := Task{ID: "t"}.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON = %v", err)
	}
	if bytes.HasSuffix(raw, []byte("\n")) {
		t.Errorf("MarshalJSON ends with a newline: %q", raw)
	}
}
