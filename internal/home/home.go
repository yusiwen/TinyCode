// Package home answers "which directory is home?" one way for everything that
// needs it.
//
// HOME wins when it is set. Tests, containers, services and Git Bash on Windows
// all name home that way, and on Windows os.UserHomeDir answers with USERPROFILE
// instead — a different directory than the one the person's shell, their `~`, and
// a test that redirected HOME are talking about. Preferring HOME everywhere is
// what keeps "~/.tinycode" one path instead of two.
package home

import "os"

// Dir returns the directory to treat as home: HOME when it is set, otherwise the
// platform's answer.
func Dir() (string, error) {
	if dir := os.Getenv("HOME"); dir != "" {
		return dir, nil
	}
	return os.UserHomeDir()
}
