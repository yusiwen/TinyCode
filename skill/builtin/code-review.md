---
name: code-review
description: Review the diff of the last commit and provide a code change summary.
builtin: true
---

## Steps

1. Run `git diff --stat HEAD~1` to see which files changed and how many lines were added/removed.
2. For each modified file, use `read_file` to understand the context of the changes.
3. Run `git diff HEAD~1` to get the full diff of the last commit.
4. Review the changes for:
   - Missing error handling in Go (unchecked `err` returns)
   - Unused imports or variables
   - Missing test coverage for new code
   - Breaking changes to public APIs or function signatures
   - Hardcoded values that should be configuration
5. Summarize your findings to the user in a clear, structured format.

## Notes

- This skill reviews the **last commit** only. For uncommitted changes, use the `git-commit` skill.
- The review is informational only — do not modify files without user confirmation.
