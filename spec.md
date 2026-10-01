# dependabot-ram

A Go script that automatically rebases, approves, and squash-merges open Dependabot PRs one at a time, oldest first.

## Overview

1. Find all open Dependabot PRs on the repo using the GitHub CLI
2. Sort by PR number ascending (oldest first)
3. Process each PR in order using the steps below

## Per-PR processing

### 1. Existence check
Check the PR is still open (Dependabot may have closed it since the list was fetched). If it is no longer open, skip to the next PR.

### 2. Rebase check (type-dependent)

Determine the PR type from its branch name:

**GitHub Actions bump** (`dependabot/github_actions/` branch prefix):
- Check `mergeStateStatus` using the GitHub CLI
- If `DIRTY` (merge conflict):
  - Comment `@dependabot rebase` on the PR
  - Wait for the rebase to complete using the same polling logic as node packages (20s interval, 15 minute timeout)
  - If still dirty after the rebase, skip to the next PR
- Otherwise proceed to step 3

**Node package bump** (all other Dependabot branches):
- Use `git` to check whether the default branch is an ancestor of the PR branch (i.e. the PR is up-to-date)
- If the PR is behind:
  - If the PR body contains `Dependabot is rebasing this PR`, a rebase is already in progress — skip requesting one
  - Otherwise comment `@dependabot rebase` on the PR
  - Poll every 20s until the rebase is complete. The rebase is considered complete when any of the following is true:
    - `git` confirms the default branch is now an ancestor of the PR branch
    - The PR body no longer contains `Dependabot is rebasing this PR`
    - Dependabot comments `Looks like this PR is already up-to-date with <branch>!`
  - Timeout after 15 minutes and proceed anyway

### 3. Wait for checks

Poll every 10s (timeout 60 minutes) until all PR checks reach a terminal state.

Check state classification (case-insensitive):

| State | Classification |
|---|---|
| `success`, `neutral`, `skipped` | Passed |
| `fail`, `failure`, `error`, `cancelled`, `timed_out`, `action_required`, `startup_failure`, `stale` | Failed |
| Anything else | Still running |

If any checks fail: print the failed check names and exit with a non-zero exit code.

### 4. Approval

Check `reviewDecision` using the GitHub CLI. If the PR is not already approved, approve it.

### 5. Merge

Squash-merge the PR using the default summary, with a blank body.

If the merge fails: print the error and exit with a non-zero exit code.
