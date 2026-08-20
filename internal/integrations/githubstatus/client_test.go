package githubstatus

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type scriptedStep struct {
	result Result
	err    error
}

type scriptedRunner struct {
	steps       []scriptedStep
	invocations []Invocation
}

func (runner *scriptedRunner) Run(_ context.Context, invocation Invocation) (Result, error) {
	runner.invocations = append(runner.invocations, invocation)
	if len(runner.steps) == 0 {
		return Result{}, errors.New("unexpected invocation")
	}
	step := runner.steps[0]
	runner.steps = runner.steps[1:]
	return step.result, step.err
}

func TestActiveAccountUsesMetadataOnlyAuthStatus(t *testing.T) {
	runner := &scriptedRunner{steps: []scriptedStep{{result: Result{Stdout: []byte(`{
		"hosts":{"github.com":[{"active":true,"host":"github.com","login":"theronburger","state":"success"}]}
	}`)}}}}
	client, err := NewCLI("/gh", runner, "github.com")
	if err != nil {
		t.Fatal(err)
	}
	account, err := client.ActiveAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if account != "theronburger" {
		t.Fatalf("account: got %q", account)
	}
	want := []string{"auth", "status", "--active", "--hostname", "github.com", "--json", "hosts"}
	if !reflect.DeepEqual(runner.invocations[0].Arguments, want) {
		t.Fatalf("arguments: got %#v, want %#v", runner.invocations[0].Arguments, want)
	}
	for _, argument := range runner.invocations[0].Arguments {
		if argument == "--show-token" {
			t.Fatal("auth command requested token material")
		}
	}
}

func TestPullRequestReturnsFullMetadataAndChecks(t *testing.T) {
	runner := &scriptedRunner{steps: []scriptedStep{
		{result: Result{Stdout: []byte(`[
			{"number":7,"state":"CLOSED","createdAt":"2026-08-01T10:00:00Z","updatedAt":"2026-08-03T10:00:00Z"},
			{"number":8,"state":"OPEN","createdAt":"2026-08-02T10:00:00Z","updatedAt":"2026-08-04T10:00:00Z"}
		]`)}},
		{result: Result{Stdout: []byte(`{
			"number":8,"title":"Make imports deterministic","url":"https://github.com/example/sample/pull/8",
			"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED",
			"reviewDecision":"REVIEW_REQUIRED","baseRefName":"main","headRefName":"PROJ-830/imports",
			"headRefOid":"0123456789abcdef0123456789abcdef01234567",
			"createdAt":"2026-08-02T10:00:00Z","updatedAt":"2026-08-04T10:00:00Z",
			"closedAt":null,"mergedAt":null
		}`)}},
		{result: Result{ExitCode: 8, Stdout: []byte(`[
			{"bucket":"pending","name":"unit","state":"IN_PROGRESS","workflow":"CI","link":"https://github.com/example/sample/actions/runs/2","startedAt":"2026-08-04T10:01:00Z","completedAt":null},
			{"bucket":"pass","name":"lint","state":"SUCCESS","workflow":"CI","link":"https://github.com/example/sample/actions/runs/1","startedAt":"2026-08-04T10:00:00Z","completedAt":"2026-08-04T10:02:00Z"}
		]`)}},
	}}
	client, _ := NewCLI("/gh", runner, "github.com")
	pullRequest, found, err := client.PullRequest(context.Background(), "example/sample", "PROJ-830/imports")
	if err != nil {
		t.Fatal(err)
	}
	if !found || pullRequest == nil {
		t.Fatal("pull request was not found")
	}
	if pullRequest.Number != 8 || pullRequest.Mergeable != "mergeable" ||
		pullRequest.MergeState != "blocked" || pullRequest.ReviewDecision != "review_required" {
		t.Fatalf("unexpected pull request: %#v", pullRequest)
	}
	if pullRequest.Checks.State != "pending" || pullRequest.Checks.Total != 2 ||
		pullRequest.Checks.Passing != 1 || pullRequest.Checks.Pending != 1 {
		t.Fatalf("unexpected checks: %#v", pullRequest.Checks)
	}
	if got := runner.invocations[0].Arguments; !reflect.DeepEqual(got, []string{
		"pr", "list", "--repo", "example/sample", "--head", "PROJ-830/imports",
		"--state", "all", "--limit", "10", "--json", "number,state,createdAt,updatedAt",
	}) {
		t.Fatalf("list arguments: %#v", got)
	}
	if got := runner.invocations[1].Arguments; !reflect.DeepEqual(got, []string{
		"pr", "view", "8", "--repo", "example/sample", "--json",
		"number,title,url,state,isDraft,mergeable,mergeStateStatus,reviewDecision,baseRefName,headRefName,headRefOid,createdAt,updatedAt,closedAt,mergedAt",
	}) {
		t.Fatalf("view arguments: %#v", got)
	}
}

func TestPullRequestReturnsNoMatchWithoutExtraQueries(t *testing.T) {
	runner := &scriptedRunner{steps: []scriptedStep{{result: Result{Stdout: []byte(`[]`)}}}}
	client, _ := NewCLI("/gh", runner, "github.com")
	pullRequest, found, err := client.PullRequest(context.Background(), "example/sample", "main")
	if err != nil || found || pullRequest != nil {
		t.Fatalf("result: pull request=%#v found=%v error=%v", pullRequest, found, err)
	}
	if len(runner.invocations) != 1 {
		t.Fatalf("invocations: got %d, want 1", len(runner.invocations))
	}
}

func TestPullRequestPreservesMetadataWhenChecksAreUnavailable(t *testing.T) {
	runner := &scriptedRunner{steps: []scriptedStep{
		{result: Result{Stdout: []byte(`[{
			"number":9,"state":"MERGED","createdAt":"2026-08-01T10:00:00Z","updatedAt":"2026-08-05T10:00:00Z"
		}]`)}},
		{result: Result{Stdout: []byte(`{
			"number":9,"title":"Merged work","url":"https://github.com/example/sample/pull/9",
			"state":"MERGED","isDraft":false,"mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN",
			"reviewDecision":"APPROVED","baseRefName":"main","headRefName":"PROJ-9",
			"headRefOid":"0123456789abcdef0123456789abcdef01234567",
			"createdAt":"2026-08-01T10:00:00Z","updatedAt":"2026-08-05T10:00:00Z",
			"closedAt":"2026-08-05T10:00:00Z","mergedAt":"2026-08-05T10:00:00Z"
		}`)}},
		{result: Result{ExitCode: 1}},
	}}
	client, _ := NewCLI("/gh", runner, "github.com")
	pullRequest, found, err := client.PullRequest(context.Background(), "example/sample", "PROJ-9")
	if err != nil || !found {
		t.Fatalf("found=%v error=%v", found, err)
	}
	if pullRequest.State != "merged" || pullRequest.Mergeable != "not_applicable" ||
		pullRequest.Checks.State != "unavailable" || pullRequest.Checks.Items == nil {
		t.Fatalf("unexpected merged pull request: %#v", pullRequest)
	}
}

func TestPullRequestRejectsHostileMetadata(t *testing.T) {
	runner := &scriptedRunner{steps: []scriptedStep{
		{result: Result{Stdout: []byte(`[{
			"number":8,"state":"OPEN","createdAt":"2026-08-01T10:00:00Z","updatedAt":"2026-08-04T10:00:00Z"
		}]`)}},
		{result: Result{Stdout: []byte(`{
			"number":8,"title":"A pull request","url":"https://lookalike.example/example/sample/pull/8",
			"state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",
			"reviewDecision":"APPROVED","baseRefName":"main","headRefName":"PROJ-8",
			"headRefOid":"0123456789abcdef0123456789abcdef01234567",
			"createdAt":"2026-08-01T10:00:00Z","updatedAt":"2026-08-04T10:00:00Z",
			"closedAt":null,"mergedAt":null
		}`)}},
		{result: Result{Stdout: []byte(`[]`)}},
	}}
	client, _ := NewCLI("/gh", runner, "github.com")
	_, _, err := client.PullRequest(context.Background(), "example/sample", "PROJ-8")
	if !errors.Is(err, ErrResponseInvalid) {
		t.Fatalf("error: got %v, want %v", err, ErrResponseInvalid)
	}
}
