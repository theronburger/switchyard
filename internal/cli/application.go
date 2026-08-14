package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
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
	StartEnvironment(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error)
	StopEnvironment(context.Context, string, contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error)
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

func (b LiveBackend) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.StartEnvironment(ctx, request)
}

func (b LiveBackend) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.StopEnvironment(ctx, environmentID, request)
}

type Application struct {
	Backend      Backend
	Stdout       io.Writer
	Stderr       io.Writer
	NewRequestID func() (string, error)
}

type parsedCommand struct {
	Name             string
	JSON             bool
	Positionals      []string
	IdempotencyKey   string
	ExpectedRevision *int64
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

	command, ok := parseArguments(arguments)
	if !ok {
		writeUsage(stderr)
		return ExitUsage
	}
	switch command.Name {
	case "status":
		snapshot, err := a.Backend.Status(ctx)
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		if command.JSON {
			return encodeJSON(stdout, snapshot)
		}
		writeStatusText(stdout, snapshot)
		return ExitSuccess
	case "doctor":
		report := a.Backend.Doctor(ctx)
		if command.JSON {
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
	case "start":
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := a.Backend.StartEnvironment(ctx, contractv1.StartEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
				IdempotencyKey: idempotencyKey, ExpectedEnvironmentRevision: command.ExpectedRevision,
			},
			WorktreeID: command.Positionals[0], ServiceIDs: append([]string(nil), command.Positionals[1:]...),
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "stop":
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := a.Backend.StopEnvironment(ctx, command.Positionals[0], contractv1.StopEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
				IdempotencyKey: idempotencyKey, ExpectedEnvironmentRevision: command.ExpectedRevision,
			},
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	default:
		return ExitUsage
	}
}

func parseArguments(arguments []string) (parsedCommand, bool) {
	if len(arguments) < 1 {
		return parsedCommand{}, false
	}
	command := parsedCommand{Name: arguments[0]}
	if command.Name != "status" && command.Name != "doctor" &&
		command.Name != "start" && command.Name != "stop" {
		return parsedCommand{}, false
	}
	for index := 1; index < len(arguments); index++ {
		argument := arguments[index]
		switch argument {
		case "--json":
			if command.JSON {
				return parsedCommand{}, false
			}
			command.JSON = true
		case "--idempotency-key":
			if command.IdempotencyKey != "" || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			command.IdempotencyKey = arguments[index]
		case "--expected-revision":
			if command.ExpectedRevision != nil || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			revision, err := strconv.ParseInt(arguments[index], 10, 64)
			if err != nil || revision < 0 {
				return parsedCommand{}, false
			}
			command.ExpectedRevision = &revision
		default:
			if strings.HasPrefix(argument, "-") {
				return parsedCommand{}, false
			}
			command.Positionals = append(command.Positionals, argument)
		}
	}
	switch command.Name {
	case "status", "doctor":
		if len(command.Positionals) != 0 || command.IdempotencyKey != "" || command.ExpectedRevision != nil {
			return parsedCommand{}, false
		}
	case "start":
		if len(command.Positionals) < 2 || len(command.Positionals) > 33 {
			return parsedCommand{}, false
		}
	case "stop":
		if len(command.Positionals) != 1 {
			return parsedCommand{}, false
		}
	}
	return command, true
}

func (a Application) requestID() (string, error) {
	if a.NewRequestID != nil {
		return a.NewRequestID()
	}
	contents := make([]byte, 16)
	if _, err := rand.Read(contents); err != nil {
		return "", err
	}
	return "request_" + base64.RawURLEncoding.EncodeToString(contents), nil
}

func writeReceipt(writer io.Writer, receipt contractv1.MutationReceipt, jsonOutput bool) int {
	if jsonOutput {
		return encodeJSON(writer, receipt)
	}
	if receipt.EnvironmentID == "" {
		fmt.Fprintf(writer, "Accepted operation %s.\n", receipt.OperationID)
	} else {
		fmt.Fprintf(writer, "Accepted operation %s for environment %s.\n", receipt.OperationID, receipt.EnvironmentID)
	}
	return ExitSuccess
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  switchyard status [--json]")
	fmt.Fprintln(writer, "  switchyard doctor [--json]")
	fmt.Fprintln(writer, "  switchyard start <worktree-id> <service-id>... [--expected-revision N] [--idempotency-key KEY] [--json]")
	fmt.Fprintln(writer, "  switchyard stop <environment-id> [--expected-revision N] [--idempotency-key KEY] [--json]")
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
