package apiclient

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

type ErrorCode string

const (
	ErrorRuntimeDescriptorUnavailable ErrorCode = "RUNTIME_DESCRIPTOR_UNAVAILABLE"
	ErrorRuntimeDescriptorUnsafe      ErrorCode = "RUNTIME_DESCRIPTOR_UNSAFE"
	ErrorRuntimeDescriptorInvalid     ErrorCode = "RUNTIME_DESCRIPTOR_INVALID"
	ErrorRuntimeDescriptorStale       ErrorCode = "RUNTIME_DESCRIPTOR_STALE"
	ErrorRuntimeEndpointUnsafe        ErrorCode = "RUNTIME_ENDPOINT_UNSAFE"
	ErrorRuntimeVersionMismatch       ErrorCode = "RUNTIME_VERSION_MISMATCH"
	ErrorRuntimeTokenUnavailable      ErrorCode = "RUNTIME_TOKEN_UNAVAILABLE"
	ErrorRuntimeTokenUnsafe           ErrorCode = "RUNTIME_TOKEN_UNSAFE"
	ErrorRuntimeTokenInvalid          ErrorCode = "RUNTIME_TOKEN_INVALID"
	ErrorDaemonUnavailable            ErrorCode = "DAEMON_UNAVAILABLE"
	ErrorDaemonUnauthorized           ErrorCode = "DAEMON_UNAUTHORIZED"
	ErrorDaemonUnknown                ErrorCode = "DAEMON_UNKNOWN"
	ErrorDaemonIncompatible           ErrorCode = "DAEMON_INCOMPATIBLE"
	ErrorUpgradeRequired              ErrorCode = contractv2.UpgradeRequiredCode
	ErrorDaemonResponseInvalid        ErrorCode = "DAEMON_RESPONSE_INVALID"
	ErrorDaemonStatusInvalid          ErrorCode = "DAEMON_STATUS_INVALID"
	ErrorActionRequestInvalid         ErrorCode = "ACTION_REQUEST_INVALID"
	ErrorWaitTimeout                  ErrorCode = "WAIT_TIMEOUT"
	ErrorUnknown                      ErrorCode = "UNKNOWN"
)

type CodedError struct {
	Code     ErrorCode
	Contract *contractv2.ContractError
	err      error
}

func newContractError(contractError contractv2.ContractError, err error) error {
	copy := sanitizeContractError(contractError)
	return &CodedError{Code: ErrorCode(contractError.Code), Contract: &copy, err: err}
}

func sanitizeContractError(contractError contractv2.ContractError) contractv2.ContractError {
	if !safeAgentText(contractError.Message, 2048) || sensitiveAgentText(contractError.Message) {
		contractError.Message = "Switchyard rejected the requested operation."
	}
	for field, maximum := range map[*string]int{
		&contractError.ResourceKind: 256, &contractError.ResourceID: 256,
		&contractError.CurrentState: 256, &contractError.RequestedState: 256,
		&contractError.Phase: 256, &contractError.Step: 512,
		&contractError.NextAction: 256,
	} {
		if *field != "" && !safeAgentText(*field, maximum) {
			*field = ""
		}
	}
	if contractError.Diagnostic != "" &&
		(!safeAgentText(contractError.Diagnostic, 2048) || sensitiveAgentText(contractError.Diagnostic)) {
		contractError.Diagnostic = ""
	}
	if contractError.LogReference != "" &&
		(!safeAgentText(contractError.LogReference, 1024) || strings.HasPrefix(contractError.LogReference, "/") ||
			strings.Contains(contractError.LogReference, "..") || strings.Contains(contractError.LogReference, "\\")) {
		contractError.LogReference = ""
	}
	if contractError.ExitCode != nil && (*contractError.ExitCode < -1 || *contractError.ExitCode > 255) {
		contractError.ExitCode = nil
	}
	return contractError
}

func safeAgentText(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\t' {
			return false
		}
	}
	return true
}

func sensitiveAgentText(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"/users/", "authorization:", "bearer ", "bearer-", "password=", "token=", "secret=", "api_key=", "api-key=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func newCodedError(code ErrorCode, err error) error {
	return &CodedError{Code: code, err: err}
}

func (e *CodedError) Error() string {
	return string(e.Code)
}

func (e *CodedError) Unwrap() error {
	return e.err
}

func CodeOf(err error) ErrorCode {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ErrorUnknown
}

func ContractErrorOf(err error) (contractv2.ContractError, bool) {
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Contract == nil {
		return contractv2.ContractError{}, false
	}
	return *coded.Contract, true
}
