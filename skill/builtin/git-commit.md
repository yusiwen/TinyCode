---
name: git-commit
description: Show staged changes and generate a commit message for review.
builtin: true
---

## Steps

1. Run `git diff --stat --cached` to see what files are staged.
2. Run `git status --short` to see the full working tree status (staged + unstaged).
3. Based on the changes, propose a descriptive commit message.
4. Present the staged changes and proposed message to the user.
5. Ask the user to confirm with `confirm` or provide a revised message.

## Notes

- This skill does NOT actually commit. It only shows what would be committed.
- The user must explicitly confirm before any commit is made.
- If files are not staged yet, guide the user to stage them first with `git add`.
