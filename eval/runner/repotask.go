package main

// Repository-level evaluation tasks (roadmap F1's "the domain, not the difficulty
// knob").
//
// A task here is not a hand-written module: it is this repository at some commit,
// with a fix removed, graded by the tests the fix added. The tree is materialized
// from git history at run time, so no copy of it lives in the repository, and the
// tests are injected only after the attempt — the agent never sees the grade.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// repoTask describes one repository-level task. Its directory holds a TASK file
// (base:, fix:, packages:, hidden:) and a PROMPT.
type repoTask struct {
	Name     string
	Dir      string
	Base     string   // the commit whose tree the agent gets (the bug state)
	Fix      string   // the commit that fixed it (the tests come from here)
	Packages []string // what grading runs: "go test <packages>"
	Hidden   []string // test files, taken from Fix, injected after the run
}

// loadRepoTasks reads every task directory under dir, with files read through git
// in repo.
func loadRepoTasks(dir, repo string) ([]task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read repo tasks: %w", err)
	}
	var tasks []task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		spec, err := readRepoTask(path)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", entry.Name(), err)
		}
		for _, sha := range []string{spec.Base, spec.Fix} {
			if _, err := gitOutput(repo, "cat-file", "-e", sha); err != nil {
				return nil, fmt.Errorf("task %s: commit %s is not in %s: %w", entry.Name(), sha, repo, err)
			}
		}
		prompt, err := os.ReadFile(filepath.Join(path, "PROMPT"))
		if err != nil {
			return nil, fmt.Errorf("task %s has no PROMPT: %w", entry.Name(), err)
		}
		tasks = append(tasks, task{
			Name:   entry.Name(),
			Dir:    path,
			Prompt: strings.TrimSpace(string(prompt)),
			Repo:   &repoSpec{Repo: repo, Base: spec.Base, Fix: spec.Fix, Packages: spec.Packages, Hidden: spec.Hidden},
		})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no repo tasks in %s", dir)
	}
	return tasks, nil
}

// repoTaskSpec is the TASK file, parsed.
type repoTaskSpec struct {
	Base     string
	Fix      string
	Packages []string
	Hidden   []string
}

// readRepoTask parses the TASK file: `key: value` lines, where packages and
// hidden are space-separated lists.
func readRepoTask(dir string) (repoTaskSpec, error) {
	data, err := os.ReadFile(filepath.Join(dir, "TASK"))
	if err != nil {
		return repoTaskSpec{}, err
	}
	var spec repoTaskSpec
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return repoTaskSpec{}, fmt.Errorf("TASK line %q is not key: value", line)
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "base":
			spec.Base = value
		case "fix":
			spec.Fix = value
		case "packages":
			spec.Packages = strings.Fields(value)
		case "hidden":
			spec.Hidden = strings.Fields(value)
		default:
			return repoTaskSpec{}, fmt.Errorf("unknown TASK key %q", key)
		}
	}
	if spec.Base == "" || spec.Fix == "" || len(spec.Packages) == 0 || len(spec.Hidden) == 0 {
		return repoTaskSpec{}, fmt.Errorf("TASK needs base, fix, packages and hidden")
	}
	return spec, nil
}

// materializeRepoTask writes the base tree into dir. Everything goes through git,
// so the tree is exactly the commit's.
//
// The tests are *not* injected here: they are the grade, and a work copy that
// already carried them would let the agent read the assertion it has to satisfy.
// The attempt injects them after the run, the way the spike's -hidden does; only
// the offline gate injects them first, because its question is "is it red".
func materializeRepoTask(spec *repoSpec, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	archive := exec.Command("git", "-C", spec.Repo, "archive", spec.Base)
	tar := exec.Command("tar", "-x", "-C", dir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	tar.Stdin = pipe
	if err := tar.Start(); err != nil {
		return err
	}
	if err := archive.Run(); err != nil {
		return fmt.Errorf("git archive %s: %w", spec.Base, err)
	}
	if err := tar.Wait(); err != nil {
		return fmt.Errorf("tar extract: %w", err)
	}
	return nil
}

// injectRepoTaskTests writes the fix commit's test files into the work copy.
func injectRepoTaskTests(spec *repoSpec, dir string) error {
	for _, path := range spec.Hidden {
		data, err := gitOutput(spec.Repo, "show", spec.Fix+":"+path)
		if err != nil {
			return fmt.Errorf("read %s from %s: %w", path, spec.Fix, err)
		}
		target := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(data), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// repoSolutionPatch is the fix's non-test diff: what the offline validation
// applies to prove the task is solvable.
//
// The output is read raw, not through gitOutput: a patch whose last line has no
// newline is a corrupt patch to `git apply`, and trimming it is how this
// function first failed.
func repoSolutionPatch(spec *repoSpec) (string, error) {
	out, err := exec.Command("git", "-C", spec.Repo, "diff", spec.Base, spec.Fix, "--", "*.go", ":!*_test.go").Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s %s: %w", spec.Base, spec.Fix, err)
	}
	if !strings.HasSuffix(string(out), "\n") {
		return string(out) + "\n", nil
	}
	return string(out), nil
}

// gradeRepoTask runs the task's packages in the work copy.
func gradeRepoTask(dir string, packages []string) bool {
	args := append([]string{"test"}, packages...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// gitOutput runs one git command in repo and returns its stdout.
func gitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
