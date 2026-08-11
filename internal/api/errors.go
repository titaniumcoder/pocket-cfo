// Package api is the Hermes-facing service: plain Go methods over the
// existing cached loaders, with no knowledge of HTTP. The REST and MCP
// adapters translate; they never compute.
package api

import "fmt"

// Error codes. Both adapters map from these, which is what stops the REST and
// MCP surfaces drifting apart.
const (
	CodeInvalidRequest     = "invalid_request"
	CodeValidationFailed   = "validation_failed"
	CodeWouldRemove        = "would_remove"
	CodeNotFound           = "not_found"
	CodeConflict           = "conflict"
	CodeWriteNotConfigured = "write_not_configured"
	CodeUpstream           = "upstream"
	CodeInternal           = "internal"
)

// Error is the only error type the service returns.
type Error struct {
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
