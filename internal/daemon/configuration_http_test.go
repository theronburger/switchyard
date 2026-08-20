package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

type configurationActionBackend struct {
	status   contractv1.ConfigurationStatus
	validate func(contractv1.ConfigurationValidationRequest) contractv1.ConfigurationStatus
	accept   func(contractv1.ConfigurationAcceptanceRequest) contractv1.ConfigurationStatus
}

func (backend configurationActionBackend) Status(context.Context) (contractv1.ConfigurationStatus, error) {
	return backend.status, nil
}

func (backend configurationActionBackend) Validate(_ context.Context, request contractv1.ConfigurationValidationRequest) (contractv1.ConfigurationStatus, error) {
	return backend.validate(request), nil
}

func (backend configurationActionBackend) Accept(_ context.Context, request contractv1.ConfigurationAcceptanceRequest) (contractv1.ConfigurationStatus, error) {
	return backend.accept(request), nil
}

func TestConfigurationHTTPValidatesAndAcceptsExactCandidate(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	sourceDigest := "sha256:" + strings.Repeat("b", 64)
	backend := configurationActionBackend{
		status: contractv1.ConfigurationStatus{SchemaVersion: 1, State: "missing"},
		validate: func(request contractv1.ConfigurationValidationRequest) contractv1.ConfigurationStatus {
			if request.ExpectedRevision != 0 {
				t.Fatalf("validation: %+v", request)
			}
			return contractv1.ConfigurationStatus{SchemaVersion: 1, State: "pending", Candidate: &contractv1.ConfigurationCandidate{
				SchemaVersion: 1, Digest: digest, SourceDigest: sourceDigest, CompilerVersion: "compiler-v1",
				RepositoryDigests: map[string]string{"sample": digest}, ExecutableDigests: map[string]string{}, StagedAt: time.Now().UTC(),
			}}
		},
		accept: func(request contractv1.ConfigurationAcceptanceRequest) contractv1.ConfigurationStatus {
			if request.ExpectedRevision != 0 || request.Digest != digest {
				t.Fatalf("acceptance: %+v", request)
			}
			return contractv1.ConfigurationStatus{SchemaVersion: 1, State: "accepted", AcceptedRevision: 1, AcceptedDigest: digest}
		},
	}
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now().UTC(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Configuration: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	validate := configurationRequest(handler, "/v1/configuration/validate", `{"schemaVersion":1,"expectedRevision":0}`)
	if validate.Code != http.StatusOK || !strings.Contains(validate.Body.String(), digest) {
		t.Fatalf("validate: %d %s", validate.Code, validate.Body.String())
	}
	accept := configurationRequest(handler, "/v1/configuration/accept", `{"schemaVersion":1,"expectedRevision":0,"digest":"`+digest+`"}`)
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
	response := configurationRequest(handler, "/v1/configuration/validate", `{"schemaVersion":1,"expectedRevision":0,"extra":true}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func configurationRequest(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
