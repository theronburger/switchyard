package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseTurboPackagesSupportsCurrentAndLegacyShapes(t *testing.T) {
	tests := []struct {
		fixture string
		want    []TurboPackage
	}{
		{
			fixture: "packages-items.json",
			want: []TurboPackage{
				{Name: "api", Path: "api"},
				{Name: "app", Path: "app"},
				{Name: "organizer", Path: "organizer"},
				{Name: "nonprofit-service", Path: "services/nonprofit-service"},
			},
		},
		{
			fixture: "packages-array.json",
			want: []TurboPackage{
				{Name: "organizer", Path: "organizer"},
				{Name: "wallet-service", Path: "services/wallet"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			packages, err := ParseTurboPackages(readTurboFixture(t, test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(packages, test.want) {
				t.Fatalf("packages:\n got: %#v\nwant: %#v", packages, test.want)
			}
		})
	}
}

func TestParseAffectedTurboPackages(t *testing.T) {
	packages, err := ParseAffectedTurboPackages(readTurboFixture(t, "start-dry-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"organizer", "nonprofit-service"}
	if !reflect.DeepEqual(packages, want) {
		t.Fatalf("packages: got %#v, want %#v", packages, want)
	}
}

func TestTurboParsersRejectMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		parse func([]byte) error
		input string
	}{
		{
			name: "invalid JSON",
			parse: func(contents []byte) error {
				_, err := ParseTurboPackages(contents)
				return err
			},
			input: `{`,
		},
		{
			name: "trailing malformed JSON",
			parse: func(contents []byte) error {
				_, err := ParseTurboPackages(contents)
				return err
			},
			input: `{"packages":[]} trailing`,
		},
		{
			name: "missing packages",
			parse: func(contents []byte) error {
				_, err := ParseTurboPackages(contents)
				return err
			},
			input: `{}`,
		},
		{
			name: "unnamed package",
			parse: func(contents []byte) error {
				_, err := ParseTurboPackages(contents)
				return err
			},
			input: `{"packages":[{"path":"organizer"}]}`,
		},
		{
			name: "missing tasks",
			parse: func(contents []byte) error {
				_, err := ParseAffectedTurboPackages(contents)
				return err
			},
			input: `{}`,
		},
		{
			name: "runnable task without command",
			parse: func(contents []byte) error {
				_, err := ParseAffectedTurboPackages(contents)
				return err
			},
			input: `{"tasks":[{"package":"organizer","task":"start"}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.parse([]byte(test.input)); err == nil {
				t.Fatal("expected malformed input to fail")
			}
		})
	}
}

func TestTurboDiscoveryUsesExactArgv(t *testing.T) {
	runner := &recordingRunner{outputs: []CommandOutput{
		{Stdout: readTurboFixture(t, "packages-items.json")},
		{Stdout: readTurboFixture(t, "start-dry-run.json")},
	}}
	discovery := NewTurboDiscovery(runner, "/repo with spaces/node_modules/.bin/turbo")

	if _, err := discovery.ListPackages(context.Background(), "/repo with spaces"); err != nil {
		t.Fatal(err)
	}
	const mergeBase = "0123456789abcdef0123456789abcdef01234567"
	if _, err := discovery.AffectedPackages(context.Background(), "/repo with spaces", mergeBase); err != nil {
		t.Fatal(err)
	}

	want := []Invocation{
		{
			Executable: "/repo with spaces/node_modules/.bin/turbo",
			Arguments: []string{
				"--no-update-notifier",
				"ls",
				"--filter=./services/*",
				"--filter=api",
				"--filter=app",
				"--filter=organizer",
				"--output=json",
			},
			WorkingDirectory: "/repo with spaces",
		},
		{
			Executable: "/repo with spaces/node_modules/.bin/turbo",
			Arguments: []string{
				"--no-update-notifier",
				"run",
				"start",
				"--dry-run=json",
				"--filter=...[" + mergeBase + "]",
			},
			WorkingDirectory: "/repo with spaces",
		},
	}
	if !reflect.DeepEqual(runner.invocations, want) {
		t.Fatalf("invocations:\n got: %#v\nwant: %#v", runner.invocations, want)
	}
}

func readTurboFixture(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "turbo", name))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
