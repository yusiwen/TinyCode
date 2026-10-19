package types

// TodoItem is one entry in a session's task list.
//
// It lives in types because the list is session state: the session file stores
// it, and the tool and the TUI both read it. The tool package aliases these
// names, so a caller that reaches for tool.TodoItem keeps working.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// TodoSummary counts a list by status.
type TodoSummary struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Cancelled  int `json:"cancelled"`
}
