# dependabot-ram

Write me a script using the latest version of Go

Use the GitHub CLI to find open dependabot PRs on the repo.

From the oldest PR (opened first or lowest PR number), to the newest PR, one by one

- Check the PR is based on the latest changes from the default branch. If not:
  - Check if a rebase is already in progress:
    - PR body will contain `Dependabot is rebasing this PR`
  - If a rebase is not in progress
    - Comment `@dependabot rebase` on the PR
  - Wait for the rebase to complete (poll at an appropriate interval), or if dependabot comments with `Looks like this PR is already up-to-date with <branch>!`, then continue
- Wait for the PR checks to complete
  - If any checks fail, print the failure and exit with non-zero exit-code
- Check if the PR needs approval to merge:
  - Provide approval if missing
- Merge the PR using squash-and-merge
  - Keep the PR title and leave the body blank
