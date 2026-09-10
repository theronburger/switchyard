package workspaces

import (
	"context"
	"reflect"
	"testing"
)

func TestRunDefaultsToOneServiceAndHonorsExplicitEmptyChoices(t *testing.T) {
	engine, config, primary, _ := engineFixture(t)
	config.Repositories[0].Setup = nil
	config.Repositories[0].Services = []ServiceConfig{
		{ID: "api", Name: "API", Kind: "api", Command: Command{Script: "sleep 30"}},
		{ID: "web", Name: "Web", Kind: "web", Command: Command{Script: "sleep 30"}},
		{ID: "worker", Name: "Worker", Kind: "worker", Command: Command{Script: "sleep 30"}},
	}
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "default-web"})
	if err != nil || !reflect.DeepEqual(run.Requested, []string{"web"}) {
		t.Fatalf("omitted selection did not choose the first web service: %+v %v", run, err)
	}
	if _, err := engine.Stop(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "explicit-empty", Services: []string{}}); err == nil {
		t.Fatal("explicitly empty selection started saved services")
	}
	if err := engine.SetChoices(primary, Choices{Target: "local", Services: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "saved-empty"}); err == nil {
		t.Fatal("saved empty selection started default services")
	}
	if _, err := engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "prepare-empty", PrepareOnly: true, Services: []string{}}); err != nil {
		t.Fatal(err)
	}
	prepared := waitEngineState(t, engine, primary, "stopped")
	if prepared.Step != "Prepared" || len(prepared.Requested) != 0 {
		t.Fatalf("empty prepare selection: %+v", prepared)
	}
	if err := engine.SetChoices(primary, Choices{Target: "local", Services: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	run, err = engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "saved-worker"})
	if err != nil || !reflect.DeepEqual(run.Requested, []string{"worker"}) {
		t.Fatalf("omitted selection ignored saved choices: %+v %v", run, err)
	}
	if _, err := engine.Stop(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	config.Choices = nil
	config.Repositories[0].Services[1].Kind = "frontend"
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	run, err = engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "default-first"})
	if err != nil || !reflect.DeepEqual(run.Requested, []string{"api"}) {
		t.Fatalf("omitted selection did not choose one first service: %+v %v", run, err)
	}
	if _, err := engine.Stop(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
}

func TestSetChoicesRejectsUnknownWorkspaceTargetAndServiceWithoutSaving(t *testing.T) {
	engine, _, primary, _ := engineFixture(t)
	wanted := Choices{Target: "local", Services: []string{"web"}}
	if err := engine.SetChoices(primary, wanted); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Choices{
		{Target: "unknown", Services: []string{"web"}},
		{Target: "local", Services: []string{"unknown"}},
		{Target: "local", Services: []string{"web", "web"}},
	} {
		if err := engine.SetChoices(primary, invalid); err == nil {
			t.Fatalf("invalid selection was saved: %+v", invalid)
		}
		if actual := engine.Config().Choices[primary]; !reflect.DeepEqual(actual, wanted) {
			t.Fatalf("failed save changed choices: %+v", actual)
		}
	}
	if err := engine.SetChoices(t.TempDir(), wanted); err == nil {
		t.Fatal("choices were saved for an unrelated directory")
	}
	loaded, err := LoadConfig(engine.configPath)
	if err != nil || !reflect.DeepEqual(loaded.Choices[primary], wanted) || len(loaded.Choices) != 1 {
		t.Fatalf("invalid choices reached disk: %+v %v", loaded.Choices, err)
	}
}
