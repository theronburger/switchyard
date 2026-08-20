package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

type configurationActionBackend struct {
	status   contractv2.ConfigurationStatus
	validate func(contractv2.ConfigurationValidationRequest) contractv2.ConfigurationStatus
	accept   func(contractv2.ConfigurationAcceptanceRequest) contractv2.ConfigurationStatus
	mutate   func(contractv2.ConfigurationRepositoryMutationRequest) (contractv2.ConfigurationStatus, error)
}

func (backend configurationActionBackend) Status(context.Context) (contractv2.ConfigurationStatus, error) {
	return backend.status, nil
}

func (backend configurationActionBackend) Validate(_ context.Context, request contractv2.ConfigurationValidationRequest) (contractv2.ConfigurationStatus, error) {
	return backend.validate(request), nil
}

func (backend configurationActionBackend) Accept(_ context.Context, request contractv2.ConfigurationAcceptanceRequest) (contractv2.ConfigurationStatus, error) {
	return backend.accept(request), nil
}

func (backend configurationActionBackend) MutateRepository(_ context.Context, request contractv2.ConfigurationRepositoryMutationRequest) (contractv2.ConfigurationStatus, error) {
	return backend.mutate(request)
}

func TestConfigurationHTTPValidatesAndAcceptsExactCandidate(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	sourceDigest := "sha256:" + strings.Repeat("b", 64)
	backend := configurationActionBackend{
		status: contractv2.ConfigurationStatus{SchemaVersion: contractv2.SchemaVersion, State: "missing"},
		validate: func(request contractv2.ConfigurationValidationRequest) contractv2.ConfigurationStatus {
			if request.ExpectedRevision != 0 {
				t.Fatalf("validation: %+v", request)
			}
			return contractv2.ConfigurationStatus{SchemaVersion: contractv2.SchemaVersion, State: "pending", Candidate: &contractv2.ConfigurationCandidate{
				SchemaVersion: contractv2.SchemaVersion, Digest: digest, SourceDigest: sourceDigest, CompilerVersion: "compiler-v1",
				RepositoryDigests: map[string]string{"sample": digest}, ExecutableDigests: map[string]string{}, StagedAt: time.Now().UTC(),
			}}
		},
		accept: func(request contractv2.ConfigurationAcceptanceRequest) contractv2.ConfigurationStatus {
			if request.ExpectedRevision != 0 || request.Digest != digest {
				t.Fatalf("acceptance: %+v", request)
			}
			return contractv2.ConfigurationStatus{SchemaVersion: contractv2.SchemaVersion, State: "accepted", AcceptedRevision: 1, AcceptedDigest: digest}
		},
	}
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now().UTC(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Configuration: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	validate := configurationRequest(handler, "/v1/configuration/validate", `{"schemaVersion":2,"expectedRevision":0}`)
	if validate.Code != http.StatusOK || !strings.Contains(validate.Body.String(), digest) {
		t.Fatalf("validate: %d %s", validate.Code, validate.Body.String())
	}
	accept := configurationRequest(handler, "/v1/configuration/accept", `{"schemaVersion":2,"expectedRevision":0,"digest":"`+digest+`"}`)
	if accept.Code != http.StatusOK || !strings.Contains(accept.Body.String(), `"acceptedRevision":1`) {
		t.Fatalf("accept: %d %s", accept.Code, accept.Body.String())
	}
}

func TestConfigurationHTTPRejectsUnknownFields(t *testing.T) {
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now().UTC(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Configuration: configurationActionBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := configurationRequest(handler, "/v1/configuration/validate", `{"schemaVersion":2,"expectedRevision":0,"extra":true}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConfigurationHTTPMapsRepositoryMutationOutcomes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	source := "sha256:" + strings.Repeat("d", 64)
	var received contractv2.ConfigurationRepositoryMutationRequest
	outcomes := map[string]error{}
	backend := configurationActionBackend{
		mutate: func(request contractv2.ConfigurationRepositoryMutationRequest) (contractv2.ConfigurationStatus, error) {
			received = request
			if err := outcomes[request.Key]; err != nil {
				return contractv2.ConfigurationStatus{}, err
			}
			return contractv2.ConfigurationStatus{
				SchemaVersion: contractv2.SchemaVersion, State: "pending",
				Candidate: &contractv2.ConfigurationCandidate{
					SchemaVersion: contractv2.SchemaVersion, Digest: digest, SourceDigest: source, CompilerVersion: "compiler",
					RepositoryDigests: map[string]string{request.Key: digest}, ExecutableDigests: map[string]string{},
					StagedAt: time.Now().UTC(),
				},
				Desired: &contractv2.ConfigurationDesiredFile{
					Present: true, SourceDigest: source,
					Repositories: []contractv2.ConfigurationRepositoryEntry{*request.Entry},
				},
			}, nil
		},
	}
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now().UTC(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Configuration: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	upsert := func(key string) string {
		return `{"schemaVersion":2,"expectedRevision":2,"expectedSourceDigest":"` + source + `","operation":"upsert","key":"` + key + `",` +
			`"entry":{"key":"` + key + `","enabled":true,"displayName":"Sample","root":"/tmp/sample","remote":"origin","defaultBase":"origin/main","managedWorktreesRoot":"/tmp/sample-worktrees"}}`
	}
	ok := configurationRequest(handler, "/v1/configuration/repositories", upsert("sample"))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"desired"`) || received.Entry == nil ||
		received.Entry.ManagedWorktreesRoot != "/tmp/sample-worktrees" || received.ExpectedSourceDigest != source {
		t.Fatalf("upsert: %d %s (received %+v)", ok.Code, ok.Body.String(), received)
	}

	outcomes["changed"] = ErrConfigurationDesiredChanged
	outcomes["bound"] = configuration.ErrRepositoryRootBound
	outcomes["enabled"] = ErrConfigurationRepositoryEnabled
	outcomes["referenced"] = ErrConfigurationRepositoryReferenced
	outcomes["missing"] = configuration.ErrRepositoryMissing
	outcomes["invalid"] = ConfigurationRejectedError{Reason: "repository \"invalid\" git remote and defaultBase are required"}
	for key, expected := range map[string]struct {
		status int
		code   string
	}{
		"changed":    {http.StatusConflict, "CONFIGURATION_DESIRED_CHANGED"},
		"bound":      {http.StatusConflict, "CONFIGURATION_ROOT_BOUND"},
		"enabled":    {http.StatusConflict, "CONFIGURATION_REPOSITORY_ENABLED"},
		"referenced": {http.StatusConflict, "CONFIGURATION_REPOSITORY_REFERENCED"},
		"missing":    {http.StatusNotFound, "CONFIGURATION_REPOSITORY_MISSING"},
		"invalid":    {http.StatusBadRequest, "CONFIGURATION_INVALID"},
	} {
		response := configurationRequest(handler, "/v1/configuration/repositories", upsert(key))
		if response.Code != expected.status || !strings.Contains(response.Body.String(), expected.code) {
			t.Errorf("%s: %d %s", key, response.Code, response.Body.String())
		}
		if key == "invalid" && !strings.Contains(response.Body.String(), "defaultBase are required") {
			t.Errorf("invalid: bounded reason was not surfaced: %s", response.Body.String())
		}
	}

	for name, body := range map[string]string{
		"remove with entry": `{"schemaVersion":2,"expectedRevision":2,"operation":"remove","key":"sample","entry":{"key":"sample","enabled":true,"displayName":"S","root":"/tmp/s","remote":"origin","defaultBase":"origin/main","managedWorktreesRoot":"/tmp/w"}}`,
		"upsert without":    `{"schemaVersion":2,"expectedRevision":2,"operation":"upsert","key":"sample"}`,
		"key mismatch":      `{"schemaVersion":2,"expectedRevision":2,"operation":"upsert","key":"sample","entry":{"key":"other","enabled":true,"displayName":"S","root":"/tmp/s","remote":"origin","defaultBase":"origin/main","managedWorktreesRoot":"/tmp/w"}}`,
		"relative root":     `{"schemaVersion":2,"expectedRevision":2,"operation":"upsert","key":"sample","entry":{"key":"sample","enabled":true,"displayName":"S","root":"tmp/s","remote":"origin","defaultBase":"origin/main","managedWorktreesRoot":"/tmp/w"}}`,
		"unknown operation": `{"schemaVersion":2,"expectedRevision":2,"operation":"repoint","key":"sample"}`,
		"bad source digest": `{"schemaVersion":2,"expectedRevision":2,"expectedSourceDigest":"abc","operation":"remove","key":"sample"}`,
		"unknown field":     `{"schemaVersion":2,"expectedRevision":2,"operation":"remove","key":"sample","force":true}`,
		"uppercase key":     `{"schemaVersion":2,"expectedRevision":2,"operation":"remove","key":"Sample"}`,
	} {
		response := configurationRequest(handler, "/v1/configuration/repositories", body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_REQUEST") {
			t.Errorf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/configuration/repositories", nil)
	get.Header.Set("Authorization", "Bearer "+testToken)
	get.Header.Set(contractv2.SchemaVersionHeader, "2")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET must not be allowed: %d", recorder.Code)
	}
}

func configurationRequest(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(contractv2.SchemaVersionHeader, "2")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
