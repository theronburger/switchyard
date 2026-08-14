package apiclient

import "errors"

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
	ErrorDaemonResponseInvalid        ErrorCode = "DAEMON_RESPONSE_INVALID"
	ErrorDaemonStatusInvalid          ErrorCode = "DAEMON_STATUS_INVALID"
	ErrorActionRequestInvalid         ErrorCode = "ACTION_REQUEST_INVALID"
	ErrorUnknown                      ErrorCode = "UNKNOWN"
)

type CodedError struct {
	Code ErrorCode
	err  error
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
