package contractv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v1", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}

	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.Environments[0].DisplayName, "DEMO-830"; got != want {
		t.Fatalf("display name: got %q, want %q", got, want)
	}
}

func TestStatusFixtureAllowsAdditiveFields(t *testing.T) {
	contents := []byte(`{
		"schemaVersion": 1,
		"snapshotRevision": 1,
		"generatedAt": "2026-08-14T10:00:00Z",
		"futureField": {"ignored": true},
		"daemon": {
			"instanceId": "daemon_test",
			"version": "test",
			"state": "ready",
			"startedAt": "2026-08-14T10:00:00Z"
		},
		"repositories": [],
		"environments": [],
		"operations": [],
		"alerts": []
	}`)

	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRejectsNullCollections(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v1", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "repositories", mutate: func(value map[string]any) { value["repositories"] = nil }},
		{name: "worktrees", mutate: func(value map[string]any) {
			value["repositories"].([]any)[0].(map[string]any)["worktrees"] = nil
		}},
		{name: "services", mutate: func(value map[string]any) {
			value["environments"].([]any)[0].(map[string]any)["services"] = nil
		}},
		{name: "urls", mutate: func(value map[string]any) {
			value["environments"].([]any)[0].(map[string]any)["urls"] = nil
		}},
		{name: "service port leases", mutate: func(value map[string]any) {
			environment := value["environments"].([]any)[0].(map[string]any)
			environment["services"].([]any)[0].(map[string]any)["portLeaseIds"] = nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(contents, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot StatusSnapshot
			if err := json.Unmarshal(mutated, &snapshot); err != nil {
				t.Fatal(err)
			}
			if err := snapshot.Validate(); err == nil {
				t.Fatal("null collection was accepted")
			}
		})
	}
}

func TestTransportFixtures(t *testing.T) {
	tests := []struct {
		name string
		file string
		into any
	}{
		{name: "runtime", file: "runtime.json", into: &RuntimeDescriptor{}},
		{name: "handshake", file: "handshake.json", into: &Handshake{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixturePath := filepath.Join("..", "..", "..", "contracts", "v1", "fixtures", test.file)
			contents, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(contents, test.into); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMutationFixtures(t *testing.T) {
	tests := []struct {
		file  string
		value any
	}{
		{
			file:  "start-environment-request.json",
			value: &StartEnvironmentRequest{},
		},
		{
			file:  "stop-environment-request.json",
			value: &StopEnvironmentRequest{},
		},
		{
			file:  "mutation-receipt.json",
			value: &MutationReceipt{},
		},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			fixturePath := filepath.Join("..", "..", "..", "contracts", "v1", "fixtures", test.file)
			contents, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(contents, test.value); err != nil {
				t.Fatal(err)
			}
			switch value := test.value.(type) {
			case *StartEnvironmentRequest:
				err = value.Validate()
			case *StopEnvironmentRequest:
				err = value.Validate()
			case *MutationReceipt:
				err = value.Validate()
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStartEnvironmentRequestRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	valid := StartEnvironmentRequest{
		MutationRequest: MutationRequest{
			SchemaVersion:  SchemaVersion,
			RequestID:      "request_test",
			IdempotencyKey: "start:test",
		},
		WorktreeID: "worktree_test",
		ServiceIDs: []string{"organizer"},
	}

	tests := []struct {
		name   string
		mutate func(*StartEnvironmentRequest)
	}{
		{name: "future schema", mutate: func(request *StartEnvironmentRequest) {
			request.SchemaVersion++
		}},
		{name: "control in request id", mutate: func(request *StartEnvironmentRequest) {
			request.RequestID = "request\nleak"
		}},
		{name: "negative revision", mutate: func(request *StartEnvironmentRequest) {
			revision := int64(-1)
			request.ExpectedEnvironmentRevision = &revision
		}},
		{name: "null services", mutate: func(request *StartEnvironmentRequest) {
			request.ServiceIDs = nil
		}},
		{name: "duplicate services", mutate: func(request *StartEnvironmentRequest) {
			request.ServiceIDs = []string{"organizer", "organizer"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.ServiceIDs = append([]string(nil), valid.ServiceIDs...)
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("invalid mutation request was accepted")
			}
		})
	}
}
