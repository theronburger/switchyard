package apiclient

import (
	"context"
	"time"
)

type CheckStatus string

const (
	CheckPass    CheckStatus = "pass"
	CheckFail    CheckStatus = "fail"
	CheckSkipped CheckStatus = "skipped"
)

type DoctorCheck struct {
	ID        string      `json:"id"`
	Status    CheckStatus `json:"status"`
	Summary   string      `json:"summary"`
	ErrorCode ErrorCode   `json:"errorCode,omitempty"`
}

type DoctorReport struct {
	SchemaVersion int           `json:"schemaVersion"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Healthy       bool          `json:"healthy"`
	Checks        []DoctorCheck `json:"checks"`
}

type Doctor struct {
	Connector Connector
	Now       func() time.Time
}

func (d Doctor) Run(ctx context.Context) DoctorReport {
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	report := DoctorReport{
		SchemaVersion: RuntimeDescriptorSchemaVersion,
		GeneratedAt:   now,
		Checks:        make([]DoctorCheck, 0, 3),
	}

	client, err := d.Connector.Client()
	if err != nil {
		report.Checks = append(report.Checks,
			failedCheck("runtime.discovery", discoverySummary(err), err),
			skippedCheck("daemon.handshake", "Handshake was not checked."),
			skippedCheck("daemon.status", "Status was not checked."))
		return report
	}
	report.Checks = append(report.Checks, DoctorCheck{
		ID:      "runtime.discovery",
		Status:  CheckPass,
		Summary: "Runtime descriptor and token are private and valid.",
	})

	if _, err := client.Handshake(ctx); err != nil {
		report.Checks = append(report.Checks,
			failedCheck("daemon.handshake", handshakeSummary(err), err),
			skippedCheck("daemon.status", "Status was not checked."))
		return report
	}
	report.Checks = append(report.Checks, DoctorCheck{
		ID:      "daemon.handshake",
		Status:  CheckPass,
		Summary: "Daemon identity and contract version are compatible.",
	})

	if _, err := client.statusAfterHandshake(ctx); err != nil {
		report.Checks = append(report.Checks,
			failedCheck("daemon.status", "The daemon did not return a valid status snapshot.", err))
		return report
	}
	report.Checks = append(report.Checks, DoctorCheck{
		ID:      "daemon.status",
		Status:  CheckPass,
		Summary: "Daemon status is readable and internally consistent.",
	})
	report.Healthy = true
	return report
}

const upgradeRequiredSummary = "The installed daemon and this client speak different contract versions; update so both match."

func discoverySummary(err error) string {
	if CodeOf(err) == ErrorUpgradeRequired {
		return upgradeRequiredSummary
	}
	return "Runtime connection files are unavailable or unsafe."
}

func handshakeSummary(err error) string {
	if CodeOf(err) == ErrorUpgradeRequired {
		return upgradeRequiredSummary
	}
	return "The installed daemon could not be authenticated."
}

func failedCheck(id, summary string, err error) DoctorCheck {
	return DoctorCheck{ID: id, Status: CheckFail, Summary: summary, ErrorCode: CodeOf(err)}
}

func skippedCheck(id, summary string) DoctorCheck {
	return DoctorCheck{ID: id, Status: CheckSkipped, Summary: summary}
}
