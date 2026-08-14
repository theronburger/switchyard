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
