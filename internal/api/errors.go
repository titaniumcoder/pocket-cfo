package api

import "fmt"

const (
	CodeInvalidRequest     = "invalid_request"
	CodeValidationFailed   = "validation_failed"
	CodeNotFound           = "not_found"
	CodeConflict           = "conflict"
	CodeWriteNotConfigured = "write_not_configured"
	CodeUpstream           = "upstream"
	CodeInternal           = "internal"
)

type Error struct {
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
