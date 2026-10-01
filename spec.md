# dependabot-ram

Write me a script using the latest version of Go

Use the GitHub CLI to find open dependabot PRs on the repo.

From the oldest PR (opened first or lowest PR number), to the newest PR, one by one

- Check the PR is based on the latest changes from the default branch. If not:
  - Comment `@dependabot rebase` on the PR
  - Wait for the rebase to happen (poll at an appropriate interval), or if dependabot comments with `Looks like this PR is already up-to-date with <branch>!`, then continue
- Wait for the PR checks to complete
- Merge the PR using squash-and-merge
  - Keep the PR title and leave the body blank
