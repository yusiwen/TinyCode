package task

import "time"

// Task is a record with three optional fields whose absence has to survive every encoding.
type Task struct {
	ID       string
	Title    string
	Due      *time.Time
	Tags     []string
	Priority int
}

// MarshalJSON returns the canonical JSON for a Task.
//
// The format is:
//
//	{"id": "...", "title": "...", "due": "2006-01-02T15:04:05Z", "tags": ["a","b"], "priority": 3}
//
// Every field is omitted when it carries no information: an empty ID, an empty Title, a nil
// Due and a nil or empty Tags are all left out, and so is a zero Priority. When Due is
// present it is encoded in UTC with second precision, using RFC3339.
//
// Map returns the same content as a map, so a caller can add fields of its own before
// encoding. The two must agree: json.Marshal(t.Map()) and t.MarshalJSON() have to produce
// JSON that decodes to the same object, for every Task, including the empty one.
func (t Task) MarshalJSON() ([]byte, error) {
	return nil, nil
}

// Map returns the Task as a map, following the same omission rules as MarshalJSON.
func (t Task) Map() map[string]any {
	return nil
}
