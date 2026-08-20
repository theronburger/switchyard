package containerhost

import (
	"context"
	"testing"
)

func TestHostDetectorReportsColimaAndDockerAvailability(t *testing.T) {
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{
		{
			command: Command{Executable: "docker-test", Arguments: []string{"context", "show"}},
			output:  "colima-sample\n",
		},
		{
			command: Command{Executable: "colima-test", Arguments: []string{"status", "sample", "--json"}},
			output:  `{"profile":"sample"}`,
		},
		{
			command: Command{Executable: "docker-test", Arguments: []string{"info", "--format", "{{json .ServerVersion}}"}},
			output:  `"28.3.2"`,
		},
	}}
	report, err := (HostDetector{
		Runner: runner, ColimaBinary: "colima-test", DockerBinary: "docker-test",
	}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if !report.Colima.Installed || !report.Colima.Running || report.Colima.Profile != "sample" {
		t.Fatalf("colima status: %+v", report.Colima)
	}
	if !report.Docker.CLIInstalled || !report.Docker.DaemonAvailable ||
		report.Docker.Context != "colima-sample" || report.Docker.ServerVersion != "28.3.2" {
		t.Fatalf("docker status: %+v", report.Docker)
	}
}

func TestHostDetectorDistinguishesInstalledButStoppedComponents(t *testing.T) {
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{
		{
			command: Command{Executable: "docker", Arguments: []string{"context", "show"}},
			output:  "colima\n",
		},
		{
			command: Command{Executable: "colima", Arguments: []string{"status", "default", "--json"}},
			err:     &CommandError{Executable: "colima", ExitCode: 1, Started: true},
		},
		{
			command: Command{Executable: "docker", Arguments: []string{"info", "--format", "{{json .ServerVersion}}"}},
			err:     &CommandError{Executable: "docker", ExitCode: 1, Started: true},
		},
	}}
	report, err := (HostDetector{Runner: runner}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Colima.Installed || report.Colima.Running {
		t.Fatalf("colima status: %+v", report.Colima)
	}
	if !report.Docker.CLIInstalled || report.Docker.DaemonAvailable {
		t.Fatalf("docker status: %+v", report.Docker)
	}
}
