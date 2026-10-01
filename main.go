package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	pollInterval  = 30 * time.Second
	rebaseTimeout = 15 * time.Minute
	checksTimeout = 60 * time.Minute
)

type PR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	Body        string `json:"body"`
}

type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type Comment struct {
	Body   string `json:"body"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

type checksFailedError struct {
	names []string
}

func (e *checksFailedError) Error() string {
	return fmt.Sprintf("%d check(s) failed: %s", len(e.names), strings.Join(e.names, ", "))
}

// fatalError signals that processing should stop immediately.
type fatalError struct{ error }

func (e *fatalError) Unwrap() error { return e.error }

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("%s", ee.Stderr)
		}
		return nil, err
	}
	return out, nil
}

func getDefaultBranch(ctx context.Context) (string, error) {
	out, err := runGH(ctx, "repo", "view", "--json", "defaultBranchRef")
	if err != nil {
		return "", fmt.Errorf("get default branch: %w", err)
	}
	var repo struct {
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal(out, &repo); err != nil {
		return "", fmt.Errorf("parse default branch: %w", err)
	}
	return repo.DefaultBranchRef.Name, nil
}

func listDependabotPRs(ctx context.Context) ([]PR, error) {
	out, err := runGH(ctx, "pr", "list",
		"--author", "app/dependabot",
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,headRefName,body",
	)
	if err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}
	var prs []PR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse PRs: %w", err)
	}
	slices.SortFunc(prs, func(a, b PR) int {
		return cmp.Compare(a.Number, b.Number)
	})
	return prs, nil
}

func isBranchUpToDate(ctx context.Context, defaultBranch, prBranch string) (bool, error) {
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", //nolint:gosec
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", defaultBranch, defaultBranch),
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", prBranch, prBranch),
	)
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git fetch: %s", strings.TrimSpace(string(out)))
	}
	err := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", //nolint:gosec
		"origin/"+defaultBranch, "origin/"+prBranch,
	).Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base: %w", err)
}

func getPRBodyAndComments(ctx context.Context, number int) (body string, comments []Comment, err error) {
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", number),
		"--json", "body,comments",
	)
	if err != nil {
		return "", nil, fmt.Errorf("view PR #%d: %w", number, err)
	}
	var state struct {
		Body     string    `json:"body"`
		Comments []Comment `json:"comments"`
	}
	if err := json.Unmarshal(out, &state); err != nil {
		return "", nil, fmt.Errorf("parse PR: %w", err)
	}
	return state.Body, state.Comments, nil
}

func waitForRebase(ctx context.Context, number int, defaultBranch, prBranch string) {
	fmt.Println("  Waiting for rebase...")
	deadline := time.Now().Add(rebaseTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		upToDate, err := isBranchUpToDate(ctx, defaultBranch, prBranch)
		if err != nil {
			fmt.Printf("  Warning (git): %v\n", err)
		} else if upToDate {
			fmt.Println("  Branch is up-to-date")
			return
		}

		body, comments, err := getPRBodyAndComments(ctx, number)
		if err != nil {
			fmt.Printf("  Warning: %v\n", err)
			fmt.Printf("  Still waiting... (%v remaining)\n", time.Until(deadline).Round(time.Minute))
			continue
		}

		for _, c := range comments {
			if c.Author.Login == "dependabot[bot]" &&
				strings.Contains(c.Body, "Looks like this PR is already up-to-date with") {
				fmt.Println("  Already up-to-date, continuing")
				return
			}
		}

		if !strings.Contains(body, "Dependabot is rebasing this PR") {
			fmt.Println("  Rebase complete, continuing")
			return
		}

		fmt.Printf("  Still waiting... (%v remaining)\n", time.Until(deadline).Round(time.Minute))
	}
	fmt.Println("  Rebase timed out — proceeding")
}

func classifyChecks(checks []Check) (pending int, failedNames []string) {
	for _, c := range checks {
		switch c.State {
		case "success", "neutral", "skipped":
			// passed
		case "fail", "failure", "error", "cancelled", "timed_out", "action_required", "startup_failure", "stale":
			failedNames = append(failedNames, c.Name)
		default:
			// pending, in_progress, queued, waiting, requested, or any unknown state
			pending++
		}
	}
	return pending, failedNames
}

func waitForChecks(ctx context.Context, number int) error {
	fmt.Println("  Waiting for checks...")
	time.Sleep(15 * time.Second)
	deadline := time.Now().Add(checksTimeout)
	for time.Now().Before(deadline) {
		out, err := runGH(ctx, "pr", "checks", fmt.Sprintf("%d", number),
			"--json", "name,state",
		)
		if err != nil {
			fmt.Println("  Checks not ready yet, retrying...")
			time.Sleep(pollInterval)
			continue
		}
		var checks []Check
		if err := json.Unmarshal(out, &checks); err != nil {
			return fmt.Errorf("parse checks: %w", err)
		}
		if len(checks) == 0 {
			fmt.Println("  No checks configured")
			return nil
		}

		pending, failedNames := classifyChecks(checks)

		if len(failedNames) > 0 {
			return &checksFailedError{names: failedNames}
		}
		if pending == 0 {
			fmt.Printf("  All %d check(s) passed\n", len(checks))
			return nil
		}
		fmt.Printf("  %d/%d check(s) still running (%v remaining)...\n",
			pending, len(checks), time.Until(deadline).Round(time.Minute))
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("checks timed out after %v", checksTimeout)
}

func ensureApproved(ctx context.Context, number int) error {
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", number),
		"--json", "reviewDecision",
	)
	if err != nil {
		return fmt.Errorf("view PR #%d: %w", number, err)
	}
	var state struct {
		ReviewDecision string `json:"reviewDecision"`
	}
	if err := json.Unmarshal(out, &state); err != nil {
		return fmt.Errorf("parse review decision: %w", err)
	}
	if state.ReviewDecision == "APPROVED" {
		fmt.Println("  PR already approved")
		return nil
	}
	fmt.Println("  Approving PR...")
	_, err = runGH(ctx, "pr", "review", fmt.Sprintf("%d", number), "--approve")
	return err
}

func mergePR(ctx context.Context, number int, title string) error {
	_, err := runGH(ctx, "pr", "merge", fmt.Sprintf("%d", number),
		"--squash",
		"--subject", title,
		"--body", "",
	)
	return err
}

func processPR(ctx context.Context, pr PR, defaultBranch string) error {
	fmt.Printf("\nPR #%d: %s\n", pr.Number, pr.Title)

	upToDate, err := isBranchUpToDate(ctx, defaultBranch, pr.HeadRefName)
	if err != nil {
		return fmt.Errorf("check branch: %w", err)
	}
	if !upToDate {
		if strings.Contains(pr.Body, "Dependabot is rebasing this PR") {
			fmt.Println("  Rebase already in progress, waiting...")
		} else {
			fmt.Println("  PR is behind base branch, requesting rebase...")
			if _, err := runGH(ctx, "pr", "comment", fmt.Sprintf("%d", pr.Number),
				"--body", "@dependabot rebase",
			); err != nil {
				return fmt.Errorf("comment: %w", err)
			}
		}
		waitForRebase(ctx, pr.Number, defaultBranch, pr.HeadRefName)
	} else {
		fmt.Println("  PR is up-to-date, skipping rebase")
	}

	if err := waitForChecks(ctx, pr.Number); err != nil {
		return &fatalError{err}
	}

	if err := ensureApproved(ctx, pr.Number); err != nil {
		return fmt.Errorf("approve: %w", err)
	}

	fmt.Println("  Merging...")
	if err := mergePR(ctx, pr.Number, pr.Title); err != nil {
		return &fatalError{fmt.Errorf("merge: %w", err)}
	}

	fmt.Printf("  Merged PR #%d\n", pr.Number)
	return nil
}

func main() {
	ctx := context.Background()

	defaultBranch, err := getDefaultBranch(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Fetching open dependabot PRs...")
	prs, err := listDependabotPRs(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(prs) == 0 {
		fmt.Println("No open dependabot PRs found.")
		return
	}

	fmt.Printf("Found %d PR(s) — processing oldest first\n", len(prs))

	var failed []int
	for _, pr := range prs {
		if err := processPR(ctx, pr, defaultBranch); err != nil {
			fmt.Fprintf(os.Stderr, "  FAILED PR #%d: %v\n", pr.Number, err)
			if _, ok := errors.AsType[*fatalError](err); ok {
				os.Exit(1)
			}
			failed = append(failed, pr.Number)
		}
	}

	fmt.Println("\nDone.")
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "Failed PRs: %v\n", failed)
		os.Exit(1)
	}
}
