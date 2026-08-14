package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

type Backend interface {
	Status(context.Context) (contractv1.StatusSnapshot, error)
	Doctor(context.Context) apiclient.DoctorReport
}

type LiveBackend struct {
	Connector apiclient.Connector
	Now       func() time.Time
}

func (b LiveBackend) Status(ctx context.Context) (contractv1.StatusSnapshot, error) {
	return b.Connector.Status(ctx)
}

func (b LiveBackend) Doctor(ctx context.Context) apiclient.DoctorReport {
	return (apiclient.Doctor{Connector: b.Connector, Now: b.Now}).Run(ctx)
}

type Application struct {
	Backend Backend
	Stdout  io.Writer
	Stderr  io.Writer
}

type errorOutput struct {
	SchemaVersion int                `json:"schemaVersion"`
	Error         errorOutputDetails `json:"error"`
}

type errorOutputDetails struct {
	Code    apiclient.ErrorCode `json:"code"`
	Message string              `json:"message"`
}

func (a Application) Run(ctx context.Context, arguments []string) int {
	stdout := a.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := a.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	if a.Backend == nil {
		fmt.Fprintln(stderr, "Switchyard client is not configured.")
		return ExitFailure
	}

	command, jsonOutput, ok := parseArguments(arguments)
	if !ok {
		fmt.Fprintln(stderr, "usage: switchyard <status|doctor> [--json]")
		return ExitUsage
	}
	switch command {
	case "status":
		snapshot, err := a.Backend.Status(ctx)
		if err != nil {
			return writeFailure(stdout, stderr, jsonOutput, err)
		}
		if jsonOutput {
			return encodeJSON(stdout, snapshot)
		}
		writeStatusText(stdout, snapshot)
		return ExitSuccess
	case "doctor":
		report := a.Backend.Doctor(ctx)
		if jsonOutput {
			if code := encodeJSON(stdout, report); code != ExitSuccess {
				return code
			}
		} else {
			writeDoctorText(stdout, report)
		}
		if !report.Healthy {
			return ExitFailure
		}
		return ExitSuccess
	default:
		return ExitUsage
	}
}

func parseArguments(arguments []string) (command string, jsonOutput bool, ok bool) {
	if len(arguments) < 1 || len(arguments) > 2 {
		return "", false, false
	}
	command = arguments[0]
	if command != "status" && command != "doctor" {
		return "", false, false
	}
	if len(arguments) == 2 {
		if arguments[1] != "--json" {
			return "", false, false
		}
		jsonOutput = true
	}
	return command, jsonOutput, true
}

func writeFailure(stdout, stderr io.Writer, jsonOutput bool, err error) int {
	code := apiclient.CodeOf(err)
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(errorOutput{
			SchemaVersion: contractv1.SchemaVersion,
			Error: errorOutputDetails{
				Code:    code,
				Message: "Switchyard could not complete the request.",
			},
		})
	} else {
		fmt.Fprintf(stderr, "Switchyard request failed (%s).\n", code)
	}
	return ExitFailure
}

func encodeJSON(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return ExitFailure
	}
	return ExitSuccess
}

func writeStatusText(writer io.Writer, snapshot contractv1.StatusSnapshot) {
	fmt.Fprintf(writer, "Switchyard revision %d\n", snapshot.SnapshotRevision)
	fmt.Fprintf(writer, "Daemon: %s (%s)\n", snapshot.Daemon.State, snapshot.Daemon.Version)
	fmt.Fprintf(writer, "Environments: %d\n", len(snapshot.Environments))

	environments := append([]contractv1.Environment(nil), snapshot.Environments...)
	sort.Slice(environments, func(left, right int) bool {
		return environments[left].DisplayName < environments[right].DisplayName
	})
	for _, environment := range environments {
		fmt.Fprintf(
			writer,
			"- %s: %s, %s, attention %d\n",
			environment.DisplayName,
			environment.ObservedState,
			environment.Health,
			len(environment.AttentionAlertIDs))
	}
}

func writeDoctorText(writer io.Writer, report apiclient.DoctorReport) {
	for _, check := range report.Checks {
		symbol := "-"
		switch check.Status {
		case apiclient.CheckPass:
			symbol = "✓"
		case apiclient.CheckFail:
			symbol = "✗"
		}
		if check.ErrorCode != "" {
			fmt.Fprintf(writer, "%s %s: %s (%s)\n", symbol, check.ID, check.Summary, check.ErrorCode)
		} else {
			fmt.Fprintf(writer, "%s %s: %s\n", symbol, check.ID, check.Summary)
		}
	}
}
