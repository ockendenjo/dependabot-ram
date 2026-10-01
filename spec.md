# dependabot-merge

Write me a script using the latest version of Go

Use the GitHub CLI to find open dependabot PRs on the repo.

From the oldest PR (opened first or lowest PR number), to the newest PR, one by one

- Comment `@dependabot rebase` on the PR
- Wait for the rebase to happen (poll at an appropriate interval)
- Wait for the PR checks to complete
- Merge the PR using squash-and-merge
  - Keep the PR title and leave the body blank
