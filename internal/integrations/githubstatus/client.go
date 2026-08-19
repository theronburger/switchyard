package githubstatus

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const (
	maximumCandidates = 10
	maximumChecks     = 128
	maximumTextBytes  = 2_048
)

var (
	ErrAuthenticationUnavailable = errors.New("github cli authentication is unavailable")
	ErrResponseInvalid           = errors.New("github cli response is invalid")
	ErrQueryFailed               = errors.New("github cli query failed")
)

type CLI struct {
	executable string
	runner     Runner
	host       string
}

func NewCLI(executable string, runner Runner, host string) (CLI, error) {
	if executable == "" || runner == nil || host == "" || strings.ContainsAny(host, "/:@ ") {
		return CLI{}, errors.New("github cli configuration is invalid")
	}
	return CLI{executable: executable, runner: runner, host: host}, nil
}

func (cli CLI) ActiveAccount(ctx context.Context) (string, error) {
	result, err := cli.run(ctx,
		"auth", "status", "--active", "--hostname", cli.host, "--json", "hosts",
	)
	if err != nil || result.ExitCode != 0 {
		return "", ErrAuthenticationUnavailable
	}
	var response struct {
		Hosts map[string][]struct {
			State  string `json:"state"`
			Active bool   `json:"active"`
			Host   string `json:"host"`
			Login  string `json:"login"`
		} `json:"hosts"`
	}
	if json.Unmarshal(result.Stdout, &response) != nil {
		return "", ErrResponseInvalid
	}
	accounts := response.Hosts[cli.host]
	for _, account := range accounts {
		if account.Active && account.State == "success" && account.Host == cli.host &&
			validText(account.Login, 256) {
			return account.Login, nil
		}
	}
	return "", ErrAuthenticationUnavailable
}

func (cli CLI) PullRequest(
	ctx context.Context,
	repository string,
	branch string,
) (*contractv1.PullRequest, bool, error) {
	if !ValidRepository(repository) || !validText(branch, maximumTextBytes) {
		return nil, false, ErrQueryFailed
	}
	candidates, err := cli.listCandidates(ctx, repository, branch)
	if err != nil {
		return nil, false, err
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}
	selected := selectCandidate(candidates)
	metadata, err := cli.view(ctx, repository, selected.Number)
	if err != nil {
		return nil, false, err
	}
	checks := cli.checks(ctx, repository, selected.Number)
	pullRequest, valid := contractPullRequest(metadata, checks, cli.host, repository)
	if !valid {
		return nil, false, ErrResponseInvalid
	}
	return &pullRequest, true, nil
}

func (cli CLI) listCandidates(
	ctx context.Context,
	repository string,
	branch string,
) ([]pullRequestCandidate, error) {
	result, err := cli.run(ctx,
		"pr", "list",
		"--repo", repository,
		"--head", branch,
		"--state", "all",
		"--limit", strconv.Itoa(maximumCandidates),
		"--json", "number,state,createdAt,updatedAt",
	)
	if err != nil || result.ExitCode != 0 {
		return nil, ErrQueryFailed
	}
	var candidates []pullRequestCandidate
	if json.Unmarshal(result.Stdout, &candidates) != nil || len(candidates) > maximumCandidates {
		return nil, ErrResponseInvalid
	}
	for _, candidate := range candidates {
		if candidate.Number < 1 || candidate.CreatedAt.IsZero() || candidate.UpdatedAt.IsZero() ||
			!oneOf(candidate.State, "OPEN", "CLOSED", "MERGED") {
			return nil, ErrResponseInvalid
		}
	}
	return candidates, nil
}

func (cli CLI) view(ctx context.Context, repository string, number int) (pullRequestMetadata, error) {
	result, err := cli.run(ctx,
		"pr", "view", strconv.Itoa(number),
		"--repo", repository,
		"--json", "number,title,url,state,isDraft,mergeable,mergeStateStatus,reviewDecision,baseRefName,headRefName,headRefOid,createdAt,updatedAt,closedAt,mergedAt",
	)
	if err != nil || result.ExitCode != 0 {
		return pullRequestMetadata{}, ErrQueryFailed
	}
	var metadata pullRequestMetadata
	if json.Unmarshal(result.Stdout, &metadata) != nil || metadata.Number != number {
		return pullRequestMetadata{}, ErrResponseInvalid
	}
	return metadata, nil
}

func (cli CLI) checks(ctx context.Context, repository string, number int) contractv1.PullRequestChecks {
	result, err := cli.run(ctx,
		"pr", "checks", strconv.Itoa(number),
		"--repo", repository,
		"--json", "bucket,name,state,workflow,link,startedAt,completedAt",
	)
	if err != nil || (result.ExitCode != 0 && result.ExitCode != 8) {
		return unavailableChecks()
	}
	var records []checkRecord
	if json.Unmarshal(result.Stdout, &records) != nil || len(records) > maximumChecks {
		return unavailableChecks()
	}
	items := make([]contractv1.PullRequestCheck, 0, len(records))
	for _, record := range records {
		if !validText(record.Name, maximumTextBytes) ||
			(record.Workflow != "" && !validText(record.Workflow, maximumTextBytes)) ||
			!validText(record.State, 256) ||
			!oneOf(record.Bucket, "pass", "fail", "pending", "skipping", "cancel") ||
			(record.Link != "" && !validHTTPSURL(record.Link, "", "")) {
			return unavailableChecks()
		}
		items = append(items, contractv1.PullRequestCheck{
			Name: record.Name, Workflow: record.Workflow, State: strings.ToLower(record.State),
			Bucket: record.Bucket, URL: record.Link,
			StartedAt: record.StartedAt, CompletedAt: record.CompletedAt,
		})
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Workflow != items[right].Workflow {
			return items[left].Workflow < items[right].Workflow
		}
		return items[left].Name < items[right].Name
	})
	return summarizeChecks(items)
}

func (cli CLI) run(ctx context.Context, arguments ...string) (Result, error) {
	return cli.runner.Run(ctx, Invocation{Executable: cli.executable, Arguments: arguments})
}

type pullRequestCandidate struct {
	Number    int       `json:"number"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type pullRequestMetadata struct {
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	State          string     `json:"state"`
	Draft          bool       `json:"isDraft"`
	Mergeable      string     `json:"mergeable"`
	MergeState     string     `json:"mergeStateStatus"`
	ReviewDecision string     `json:"reviewDecision"`
	BaseBranch     string     `json:"baseRefName"`
	HeadBranch     string     `json:"headRefName"`
	HeadRevision   string     `json:"headRefOid"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ClosedAt       *time.Time `json:"closedAt"`
	MergedAt       *time.Time `json:"mergedAt"`
}

type checkRecord struct {
	Bucket      string     `json:"bucket"`
	Name        string     `json:"name"`
	State       string     `json:"state"`
	Workflow    string     `json:"workflow"`
	Link        string     `json:"link"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
}

func selectCandidate(candidates []pullRequestCandidate) pullRequestCandidate {
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.State == "OPEN" && selected.State != "OPEN" {
			selected = candidate
			continue
		}
		if (candidate.State == "OPEN") == (selected.State == "OPEN") &&
			candidate.UpdatedAt.After(selected.UpdatedAt) {
			selected = candidate
		}
	}
	return selected
}

func contractPullRequest(
	metadata pullRequestMetadata,
	checks contractv1.PullRequestChecks,
	host string,
	repository string,
) (contractv1.PullRequest, bool) {
	state := strings.ToLower(metadata.State)
	mergeable := strings.ToLower(metadata.Mergeable)
	mergeState := strings.ToLower(metadata.MergeState)
	reviewDecision := strings.ToLower(metadata.ReviewDecision)
	if state != "open" {
		mergeable = "not_applicable"
		mergeState = "not_applicable"
	}
	if reviewDecision == "" {
		reviewDecision = "unknown"
	}
	if !oneOf(state, "open", "closed", "merged") ||
		!oneOf(mergeable, "mergeable", "conflicting", "unknown", "not_applicable") ||
		!oneOf(mergeState, "clean", "blocked", "behind", "dirty", "has_hooks", "unstable", "unknown", "not_applicable") ||
		!oneOf(reviewDecision, "approved", "changes_requested", "review_required", "unknown") ||
		metadata.Number < 1 || !validText(metadata.Title, maximumTextBytes) ||
		!validText(metadata.BaseBranch, maximumTextBytes) || !validText(metadata.HeadBranch, maximumTextBytes) ||
		!isGitObjectID(metadata.HeadRevision) || metadata.CreatedAt.IsZero() || metadata.UpdatedAt.IsZero() ||
		metadata.UpdatedAt.Before(metadata.CreatedAt) ||
		!validHTTPSURL(metadata.URL, host, "/"+repository+"/pull/"+strconv.Itoa(metadata.Number)) ||
		(state == "merged" && metadata.MergedAt == nil) {
		return contractv1.PullRequest{}, false
	}
	return contractv1.PullRequest{
		Number: metadata.Number, Title: metadata.Title, URL: metadata.URL, State: state, Draft: metadata.Draft,
		Mergeable: mergeable, MergeState: mergeState, ReviewDecision: reviewDecision,
		BaseBranch: metadata.BaseBranch, HeadBranch: metadata.HeadBranch, HeadRevision: metadata.HeadRevision,
		CreatedAt: metadata.CreatedAt, UpdatedAt: metadata.UpdatedAt,
		ClosedAt: metadata.ClosedAt, MergedAt: metadata.MergedAt, Checks: checks,
	}, true
}

func summarizeChecks(items []contractv1.PullRequestCheck) contractv1.PullRequestChecks {
	checks := contractv1.PullRequestChecks{Items: items, Total: len(items)}
	for _, item := range items {
		switch item.Bucket {
		case "pass":
			checks.Passing++
		case "fail":
			checks.Failing++
		case "pending":
			checks.Pending++
		case "skipping":
			checks.Skipping++
		case "cancel":
			checks.Cancelled++
		}
	}
	switch {
	case checks.Failing > 0:
		checks.State = "failing"
	case checks.Pending > 0:
		checks.State = "pending"
	case checks.Cancelled > 0:
		checks.State = "cancelled"
	case checks.Passing > 0:
		checks.State = "passing"
	case checks.Skipping > 0:
		checks.State = "neutral"
	default:
		checks.State = "none"
	}
	return checks
}

func unavailableChecks() contractv1.PullRequestChecks {
	return contractv1.PullRequestChecks{State: "unavailable", Items: []contractv1.PullRequestCheck{}}
}

func ValidRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !validText(part, 256) || part == "." || part == ".." || strings.ContainsAny(part, "@:?#[\\] ") {
			return false
		}
	}
	return true
}

func validText(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validHTTPSURL(value string, host string, exactPath string) bool {
	if len(value) > maximumTextBytes {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	if host != "" && parsed.Hostname() != host {
		return false
	}
	return exactPath == "" || parsed.EscapedPath() == exactPath
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrExecutableUnavailable):
		return "github_cli_unavailable"
	case errors.Is(err, ErrAuthenticationUnavailable):
		return "github_auth_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "github_timeout"
	case errors.Is(err, ErrResponseInvalid), errors.Is(err, ErrOutputTooLarge):
		return "github_response_invalid"
	default:
		return "github_query_failed"
	}
}
