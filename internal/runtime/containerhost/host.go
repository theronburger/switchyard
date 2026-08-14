package containerhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type ColimaStatus struct {
	Installed bool
	Running   bool
	Profile   string
}

type DockerStatus struct {
	CLIInstalled    bool
	DaemonAvailable bool
	Context         string
	ServerVersion   string
}

type HostStatus struct {
	Colima ColimaStatus
	Docker DockerStatus
}

type HostDetector struct {
	Runner       Runner
	ColimaBinary string
	DockerBinary string
}

func (detector HostDetector) Status(ctx context.Context) (HostStatus, error) {
	if detector.Runner == nil {
		return HostStatus{}, errors.New("container host runner is required")
	}
	colimaBinary := detector.ColimaBinary
	if colimaBinary == "" {
		colimaBinary = "colima"
	}
	dockerBinary := detector.DockerBinary
	if dockerBinary == "" {
		dockerBinary = "docker"
	}

	report := HostStatus{}
	contextOutput, contextError := detector.Runner.Run(ctx, Command{
		Executable: dockerBinary,
		Arguments:  []string{"context", "show"},
	})
	if err := ctx.Err(); err != nil {
		return HostStatus{}, err
	}
	report.Docker.CLIInstalled = commandStarted(contextError)
	if contextError == nil {
		report.Docker.CLIInstalled = true
		report.Docker.Context = singleLine(contextOutput)
	}
	report.Colima.Profile = profileFromDockerContext(report.Docker.Context)
	colimaArguments := []string{"status"}
	if report.Colima.Profile != "" {
		colimaArguments = append(colimaArguments, report.Colima.Profile)
	}
	colimaArguments = append(colimaArguments, "--json")
	colimaOutput, colimaError := detector.Runner.Run(ctx, Command{
		Executable: colimaBinary,
		Arguments:  colimaArguments,
	})
	if err := ctx.Err(); err != nil {
		return HostStatus{}, err
	}
	report.Colima.Installed = commandStarted(colimaError)
	if colimaError == nil {
		report.Colima.Installed = true
		report.Colima.Running = true
		if reportedProfile := colimaProfile(colimaOutput); reportedProfile != "" {
			report.Colima.Profile = reportedProfile
		}
	}

	versionOutput, daemonError := detector.Runner.Run(ctx, Command{
		Executable: dockerBinary,
		Arguments:  []string{"info", "--format", "{{json .ServerVersion}}"},
	})
	if err := ctx.Err(); err != nil {
		return HostStatus{}, err
	}
	if daemonError == nil {
		report.Docker.CLIInstalled = true
		report.Docker.DaemonAvailable = true
		_ = json.Unmarshal(versionOutput, &report.Docker.ServerVersion)
	}
	return report, nil
}

func commandStarted(err error) bool {
	var commandError *CommandError
	return errors.As(err, &commandError) && commandError.Started
}

func colimaProfile(contents []byte) string {
	var status struct {
		Profile string `json:"profile"`
	}
	if json.Unmarshal(contents, &status) != nil {
		return ""
	}
	return status.Profile
}

func singleLine(contents []byte) string {
	trimmed := strings.TrimSpace(string(contents))
	if strings.ContainsAny(trimmed, "\r\n\x00") {
		return ""
	}
	return trimmed
}

func profileFromDockerContext(dockerContext string) string {
	if dockerContext == "colima" {
		return "default"
	}
	if strings.HasPrefix(dockerContext, "colima-") {
		profile := strings.TrimPrefix(dockerContext, "colima-")
		if identityValuePattern.MatchString(profile) {
			return profile
		}
	}
	return ""
}
