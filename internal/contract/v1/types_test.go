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
