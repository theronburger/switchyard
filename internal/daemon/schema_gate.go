package daemon

import (
	"errors"
	"net/http"
	"strconv"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

// errSchemaVersionMismatch marks a decoded request body whose schemaVersion is
// a different contract generation. It is reported as UPGRADE_REQUIRED rather
// than a generic INVALID_REQUEST, but the body is still rejected: validation
// is not relaxed for any declared version other than the exact one.
var errSchemaVersionMismatch = errors.New("request body declares a different contract schema version")

const maximumDeclaredVersionDigits = 9

// enforceSchemaDeclaration applies the exact-version handshake rule (D-015):
// every versioned request must declare SchemaVersion exactly through the
// declaration header. The handshake itself tolerates an undeclared request so
// any client can still learn which versions the daemon supports; a handshake
// that declares a different version is told to upgrade immediately.
//
// It reports whether the request was rejected.
func enforceSchemaDeclaration(response http.ResponseWriter, request *http.Request) bool {
	values := request.Header.Values(contractv2.SchemaVersionHeader)
	if len(values) == 0 {
		if request.URL.Path == "/handshake" {
			return false
		}
		writeUpgradeRequired(response, "", "This client did not declare its contract schema version.")
		return true
	}
	if len(values) != 1 {
		writeUpgradeRequired(response, "", "This client declared more than one contract schema version.")
		return true
	}
	declared, ok := parseDeclaredSchemaVersion(values[0])
	if !ok {
		writeUpgradeRequired(response, "", "This client declared an unreadable contract schema version.")
		return true
	}
	if declared != contractv2.SchemaVersion {
		writeUpgradeRequired(response, values[0], "This client's contract schema version is not supported by the daemon.")
		return true
	}
	return false
}

func parseDeclaredSchemaVersion(value string) (int, bool) {
	if value == "" || len(value) > maximumDeclaredVersionDigits {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	if value[0] == '0' {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

// writeUpgradeRequired answers HTTP 426 with the stable UPGRADE_REQUIRED
// error. requestedVersion is the client's declaration when it was readable;
// the daemon's exact version is always published as the current state so the
// client can decide which side must be replaced.
func writeUpgradeRequired(response http.ResponseWriter, requestedVersion string, message string) {
	nextAction := "upgrade_client"
	if declared, ok := parseDeclaredSchemaVersion(requestedVersion); ok && declared > contractv2.SchemaVersion {
		nextAction = "upgrade_daemon"
	}
	writeContractError(response, http.StatusUpgradeRequired, contractv2.ContractError{
		Code:           contractv2.UpgradeRequiredCode,
		Message:        message,
		Retryable:      false,
		ResourceKind:   "contract",
		CurrentState:   strconv.Itoa(contractv2.SchemaVersion),
		RequestedState: requestedVersion,
		NextAction:     nextAction,
	})
}

// writeDecodeFailure reports a rejected request body. A body that declares a
// different contract generation is an upgrade problem; every other decoding or
// validation failure keeps the route's own stable error code.
func writeDecodeFailure(response http.ResponseWriter, err error, code string, message string) {
	if errors.Is(err, errSchemaVersionMismatch) {
		writeUpgradeRequired(response, strconv.Itoa(declaredBodySchemaVersion(err)), "This request body uses a contract schema version the daemon does not support.")
		return
	}
	writeError(response, http.StatusBadRequest, code, message, false)
}

type schemaVersionMismatchError struct {
	declared int
}

func (mismatch *schemaVersionMismatchError) Error() string { return errSchemaVersionMismatch.Error() }

func (mismatch *schemaVersionMismatchError) Is(target error) bool {
	return target == errSchemaVersionMismatch
}

func declaredBodySchemaVersion(err error) int {
	var mismatch *schemaVersionMismatchError
	if errors.As(err, &mismatch) {
		return mismatch.declared
	}
	return 0
}
