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
	Number           int    `json:"number"`
	Title            string `json:"title"`
	HeadSHA          string `json:"headRefOid"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Body             string `json:"body"`
}

type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type checksFailedError struct {
	names []string
}

func (e *checksFailedError) Error() string {
	return fmt.Sprintf("%d check(s) failed: %s", len(e.names), strings.Join(e.names, ", "))
}

type Comment struct {
	Body   string `json:"body"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

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

func listDependabotPRs(ctx context.Context) ([]PR, error) {
	out, err := runGH(ctx, "pr", "list",
		"--author", "app/dependabot",
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,headRefOid,mergeStateStatus,body",
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

func getPRState(ctx context.Context, number int) (sha string, comments []Comment, err error) {
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", number),
		"--json", "headRefOid,comments",
	)
	if err != nil {
		return "", nil, fmt.Errorf("view PR #%d: %w", number, err)
	}
	var state struct {
		HeadSHA  string    `json:"headRefOid"`
		Comments []Comment `json:"comments"`
	}
	if err := json.Unmarshal(out, &state); err != nil {
		return "", nil, fmt.Errorf("parse PR: %w", err)
	}
	return state.HeadSHA, state.Comments, nil
}

func waitForRebase(ctx context.Context, number int, beforeSHA string) {
	fmt.Printf("  Waiting for rebase (current %s)...\n", beforeSHA[:7])
	deadline := time.Now().Add(rebaseTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		sha, comments, err := getPRState(ctx, number)
		if err != nil {
			fmt.Printf("  Warning: %v\n", err)
			continue
		}
		if sha != beforeSHA {
			fmt.Printf("  Rebased (new %s)\n", sha[:7])
			return
		}
		for _, c := range comments {
			if c.Author.Login == "dependabot[bot]" &&
				strings.Contains(c.Body, "Looks like this PR is already up-to-date with") {
				fmt.Println("  Already up-to-date, continuing")
				return
			}
		}
		fmt.Printf("  Still waiting... (%v remaining)\n", time.Until(deadline).Round(time.Minute))
	}
	fmt.Println("  Rebase timed out — proceeding")
}

func waitForChecks(ctx context.Context, number int) error {
	fmt.Println("  Waiting for checks...")
	// Give GitHub a moment to register the new commits
	time.Sleep(15 * time.Second)
	deadline := time.Now().Add(checksTimeout)
	for time.Now().Before(deadline) {
		out, err := runGH(ctx, "pr", "checks", fmt.Sprintf("%d", number),
			"--json", "name,state",
		)
		if err != nil {
			fmt.Printf("  Checks not ready yet, retrying...\n")
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

		var pending int
		var failedNames []string
		for _, c := range checks {
			switch c.State {
			case "pending", "in_progress", "queued", "waiting", "requested":
				pending++
			case "fail", "failure", "error", "action_required", "startup_failure":
				failedNames = append(failedNames, c.Name)
			}
		}

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

func processPR(ctx context.Context, pr PR) error {
	fmt.Printf("\nPR #%d: %s\n", pr.Number, pr.Title)

	if pr.MergeStateStatus == "BEHIND" {
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
		waitForRebase(ctx, pr.Number, pr.HeadSHA)
	} else {
		fmt.Println("  PR is up-to-date, skipping rebase")
	}

	if err := waitForChecks(ctx, pr.Number); err != nil {
		return err
	}

	if err := ensureApproved(ctx, pr.Number); err != nil {
		return fmt.Errorf("approve: %w", err)
	}

	fmt.Println("  Merging...")
	if err := mergePR(ctx, pr.Number, pr.Title); err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	fmt.Printf("  Merged PR #%d\n", pr.Number)
	return nil
}

func main() {
	ctx := context.Background()
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
		if err := processPR(ctx, pr); err != nil {
			fmt.Fprintf(os.Stderr, "  FAILED PR #%d: %v\n", pr.Number, err)
			var cfe *checksFailedError
			if errors.As(err, &cfe) {
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
