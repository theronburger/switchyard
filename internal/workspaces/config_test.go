package workspaces

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExternalConfigChangesPreserveRunsAndRejectInvalidEdits(t *testing.T) {
	engine, config, primary, _ := engineFixture(t)
	startEngineRun(t, engine, primary, "external-config")
	running := waitEngineState(t, engine, primary, "running")
	config.Repositories[0].Name = "Edited outside the app"
	if err := SaveConfig(engine.configPath, config); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if engine.Config().Repositories[0].Name != "Edited outside the app" {
		t.Fatal("external edit was not applied")
	}
	if current := waitEngineState(t, engine, primary, "running"); current.ID != running.ID {
		t.Fatal("reload restarted the run")
	}
	if err := os.WriteFile(engine.configPath, []byte(`{"schemaVersion":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReloadConfig(); err == nil {
		t.Fatal("invalid edit was accepted")
	}
	if engine.Config().Repositories[0].Name != "Edited outside the app" {
		t.Fatal("invalid edit replaced usable configuration")
	}
	removed := Config{SchemaVersion: 1, Repositories: []RepositoryConfig{}, Choices: map[string]Choices{}}
	if err := SaveConfig(engine.configPath, removed); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReloadConfig(); err == nil {
		t.Fatal("reload hid an active workspace")
	}
	if _, err := engine.Stop(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if len(engine.Config().Repositories) != 0 {
		t.Fatal("fixed edit was not accepted after stopping")
	}
}

func TestV3ConfigurationAndStatusFixtures(t *testing.T) {
	config, err := LoadConfig(filepath.Join("..", "..", "contracts", "v3", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join("..", "..", "contracts", "v3", "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var snapshot Snapshot
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != APIVersion || len(snapshot.Repositories) != len(config.Repositories) || len(snapshot.Workspaces) != 2 {
		t.Fatal("v3 fixture has the wrong version or example shape")
	}
	for _, workspace := range snapshot.Workspaces {
		if !reflect.DeepEqual(workspace.Choices, config.Choices[workspace.Path]) {
			t.Fatal("status choices do not match configuration for the exact workspace")
		}
		if workspace.Run == nil || workspace.Run.Path != workspace.Path || workspace.Run.Target != workspace.Choices.Target ||
			!reflect.DeepEqual(workspace.Run.Requested, workspace.Choices.Services) || workspace.Run.StartedAt.IsZero() {
			t.Fatal("run is not tied to its workspace and saved choices")
		}
	}
	if snapshot.Workspaces[0].Run.State != "running" || snapshot.Workspaces[1].Run.State != "failed" ||
		len(snapshot.Workspaces[0].Run.URLs) != 2 || len(snapshot.Workspaces[1].Run.URLs) != 0 {
		t.Fatal("fixture must keep running URLs separate from the failed workspace")
	}
}

func TestConfigurationGuideExampleLoads(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "..", "docs", "CONFIGURATION.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, example, found := strings.Cut(string(guide), "```json\n")
	if !found {
		t.Fatal("configuration guide has no JSON example")
	}
	example, _, found = strings.Cut(example, "\n```")
	if !found {
		t.Fatal("configuration guide JSON example is incomplete")
	}
	path := filepath.Join(t.TempDir(), "example.json")
	if err := os.WriteFile(path, []byte(example), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRoundTripAndMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings", "config.json")
	missing, err := LoadConfig(path)
	if err != nil || missing.SchemaVersion != 1 || missing.Repositories == nil || len(missing.Repositories) != 0 || missing.Choices == nil {
		t.Fatalf("missing config: %+v, %v", missing, err)
	}
	config := sampleConfig()
	config.Repositories[0].Setup[0].Environment["PACKAGE_TOKEN"] = "private-value"
	config.Choices["/tmp/worktree"] = Choices{Target: "local", Services: []string{"web"}}
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("configuration permissions: %v, %v", info, err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("configuration changed on save: %v", err)
	}
	config.Repositories[0].DefaultTarget = "missing"
	if err := SaveConfig(path, config); err == nil {
		t.Fatal("invalid configuration replaced valid configuration")
	}
	unchanged, err := LoadConfig(path)
	if err != nil || !reflect.DeepEqual(unchanged, loaded) {
		t.Fatal("failed save changed the configuration")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remained: %v, %v", entries, err)
	}
}

func TestConfigRejectsInvalidReferencesAndDirectories(t *testing.T) {
	tests := map[string]func(*Config){
		"version":                func(c *Config) { c.SchemaVersion = 2 },
		"duplicate repository":   func(c *Config) { c.Repositories = append(c.Repositories, c.Repositories[0]) },
		"duplicate path":         func(c *Config) { r := c.Repositories[0]; r.ID = "other"; c.Repositories = append(c.Repositories, r) },
		"relative repository":    func(c *Config) { c.Repositories[0].Path = "relative" },
		"relative worktrees":     func(c *Config) { c.Repositories[0].WorktreesPath = "relative" },
		"invalid ID":             func(c *Config) { c.Repositories[0].Services[0].ID = "web.api" },
		"missing default target": func(c *Config) { c.Repositories[0].DefaultTarget = "production" },
		"duplicate target": func(c *Config) {
			c.Repositories[0].Targets = append(c.Repositories[0].Targets, c.Repositories[0].Targets[0])
		},
		"duplicate service": func(c *Config) {
			c.Repositories[0].Services = append(c.Repositories[0].Services, c.Repositories[0].Services[0])
		},
		"absolute command directory": func(c *Config) { c.Repositories[0].Setup[0].Directory = "/tmp/foreign" },
		"parent command directory":   func(c *Config) { c.Repositories[0].Setup[0].Directory = "../foreign" },
		"unclean command directory":  func(c *Config) { c.Repositories[0].Setup[0].Directory = "packages/../../foreign" },
		"empty script":               func(c *Config) { c.Repositories[0].Setup[0].Script = " " },
		"negative timeout":           func(c *Config) { c.Repositories[0].Setup[0].TimeoutSeconds = -1 },
		"invalid port":               func(c *Config) { c.Repositories[0].Services[0].Ports[0].Preferred = 65536 },
		"duplicate port":             func(c *Config) { s := &c.Repositories[0].Services[0]; s.Ports = append(s.Ports, s.Ports[0]) },
		"duplicate port variable": func(c *Config) {
			s := &c.Repositories[0].Services[0]
			s.Ports = append(s.Ports, Port{Name: "metrics", Environment: "PORT"})
		},
		"invalid port variable":   func(c *Config) { c.Repositories[0].Services[0].Ports[0].Environment = "BAD-NAME" },
		"missing readiness port":  func(c *Config) { c.Repositories[0].Services[0].Readiness.Port = "metrics" },
		"relative readiness path": func(c *Config) { c.Repositories[0].Services[0].Readiness.Path = "health" },
		"readiness timeout":       func(c *Config) { c.Repositories[0].Services[0].Readiness.TimeoutSeconds = -1 },
		"missing dependency":      func(c *Config) { c.Repositories[0].Services[0].Dependencies = []string{"other"} },
		"self dependency":         func(c *Config) { c.Repositories[0].Services[0].Dependencies = []string{"web"} },
		"dependency cycle":        func(c *Config) { c.Repositories[0].Services[1].Dependencies = []string{"web"} },
		"duplicate dependency":    func(c *Config) { c.Repositories[0].Services[0].Dependencies = []string{"api", "api"} },
		"missing port substitution": func(c *Config) {
			c.Repositories[0].Targets[0].Environment["URL"] = "http://localhost:{port:other.http}"
		},
		"incomplete port substitution": func(c *Config) { c.Repositories[0].Setup[0].Environment["PORT"] = "{port:api.http" },
		"invalid environment name":     func(c *Config) { c.Repositories[0].Setup[0].Environment["1INVALID"] = "private-value" },
		"relative choice path":         func(c *Config) { c.Choices["relative"] = Choices{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := sampleConfig()
			mutate(&config)
			if err := ValidateConfig(config); err == nil {
				t.Fatal("invalid configuration accepted")
			} else if strings.Contains(err.Error(), "private-value") {
				t.Fatal("validation error exposed environment contents")
			}
		})
	}
}

func TestConfigRejectsUnknownFieldsTrailingJSONAndOversizedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, contents := range []string{
		`{"schemaVersion":1,"repositories":[],"unknown":"private-value"}`,
		`{"schemaVersion":1,"repositories":[]} {}`,
		`{"schemaVersion":"private-value","repositories":[]}`,
		strings.Repeat("x", maximumConfigBytes+1),
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("invalid JSON error was missing or exposed contents: %v", err)
		}
	}
}

func sampleConfig() Config {
	config := Config{SchemaVersion: 1, Choices: map[string]Choices{}, Repositories: []RepositoryConfig{{
		ID: "sample", Name: "Sample", Path: "/tmp/sample", DefaultTarget: "local",
		Targets: []Target{{ID: "local", Name: "Local", Environment: map[string]string{"API_URL": "http://localhost:{port:api.http}"}}},
		Setup:   []Command{{Script: "test -d .", Directory: ".", Environment: map[string]string{"WORKSPACE": "{workspace}"}}},
		Services: []ServiceConfig{
			{ID: "web", Name: "Web", Dependencies: []string{"api"}, Command: Command{Script: "serve", Directory: "packages/web"}, Ports: []Port{{Name: "http", Environment: "PORT", Preferred: 3000, URL: true}}, Readiness: Readiness{Port: "http", Path: "/health"}},
			{ID: "api", Name: "API", Command: Command{Script: "serve"}, Ports: []Port{{Name: "http", Preferred: 8000}}},
		},
	}}}
	return config
}
