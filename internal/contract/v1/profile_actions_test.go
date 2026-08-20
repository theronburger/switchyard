package contractv1

import (
	"strings"
	"testing"
)

func validProfileAction() ProfileAction {
	return ProfileAction{
		ID: "tidy", RepositoryID: "repository_01", ProfileKey: "sample", ProfileDigest: "sha256:" + strings.Repeat("a", 64),
		DisplayName: "Tidy", Scope: "worktree", Risk: "local", Kind: "command",
	}
}

func TestProfileActionValidateEnforcesVocabulary(t *testing.T) {
	if err := validProfileAction().Validate(); err != nil {
		t.Fatal(err)
	}
	lifecycle := validProfileAction()
	lifecycle.Kind, lifecycle.Lifecycle = "lifecycle", "prepare"
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProfileAction){
		"missing digest prefix":     func(a *ProfileAction) { a.ProfileDigest = strings.Repeat("a", 64) },
		"unknown scope":             func(a *ProfileAction) { a.Scope = "global" },
		"unknown risk":              func(a *ProfileAction) { a.Risk = "destructive" },
		"unknown kind":              func(a *ProfileAction) { a.Kind = "script" },
		"command with lifecycle":    func(a *ProfileAction) { a.Lifecycle = "start" },
		"lifecycle without name":    func(a *ProfileAction) { a.Kind = "lifecycle" },
		"confirmation out of sync":  func(a *ProfileAction) { a.RequiresConfirmation = true },
		"remote-write unconfirmed":  func(a *ProfileAction) { a.Risk = "remote-write" },
		"control character in name": func(a *ProfileAction) { a.DisplayName = "Tidy\x00" },
	} {
		action := validProfileAction()
		mutate(&action)
		if action.Validate() == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	list := ProfileActionList{SchemaVersion: SchemaVersion, Actions: []ProfileAction{validProfileAction(), validProfileAction()}}
	if list.Validate() == nil {
		t.Fatal("duplicate action accepted")
	}
	if (ProfileActionList{SchemaVersion: SchemaVersion}).Validate() == nil {
		t.Fatal("nil actions collection accepted")
	}
}

func TestRunProfileActionRequestValidateBindsTargetShape(t *testing.T) {
	base := RunProfileActionRequest{
		MutationRequest: MutationRequest{SchemaVersion: SchemaVersion, RequestID: "request_01", IdempotencyKey: "key_01"},
		RepositoryID:    "repository_01", ActionID: "tidy",
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	revision := int64(3)
	for name, mutate := range map[string]func(*RunProfileActionRequest){
		"missing repository":           func(r *RunProfileActionRequest) { r.RepositoryID = "" },
		"missing action":               func(r *RunProfileActionRequest) { r.ActionID = "" },
		"worktree and environment":     func(r *RunProfileActionRequest) { r.WorktreeID, r.EnvironmentID = "w", "e" },
		"service without environment":  func(r *RunProfileActionRequest) { r.ServiceID = "s" },
		"confirmation mismatch":        func(r *RunProfileActionRequest) { r.ConfirmedActionID = "other" },
		"revision without environment": func(r *RunProfileActionRequest) { r.ExpectedEnvironmentRevision = &revision },
		"whitespace identifier":        func(r *RunProfileActionRequest) { r.WorktreeID = " w" },
		"wrong schema":                 func(r *RunProfileActionRequest) { r.SchemaVersion = 2 },
		"missing idempotency":          func(r *RunProfileActionRequest) { r.IdempotencyKey = "" },
	} {
		request := base
		mutate(&request)
		if request.Validate() == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	request := base
	request.EnvironmentID, request.ServiceID, request.ExpectedEnvironmentRevision, request.ConfirmedActionID = "e", "s", &revision, "tidy"
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}
