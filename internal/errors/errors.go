package errors

import "fmt"

type NotFoundError struct {
Resource string
ID       string
}

func (e *NotFoundError) Error() string {
return fmt.Sprintf("%s with id %s not found", e.Resource, e.ID)
}

type ConflictError struct {
Message string
}

func (e *ConflictError) Error() string {
return e.Message
}

type ValidationError struct {
Field   string
Message string
}

func (e *ValidationError) Error() string {
return fmt.Sprintf("validation error: %s - %s", e.Field, e.Message)
}

type SSHError struct {
Message string
Cause   error
}

func (e *SSHError) Error() string {
if e.Cause != nil {
return fmt.Sprintf("SSH error: %s: %v", e.Message, e.Cause)
}
return fmt.Sprintf("SSH error: %s", e.Message)
}

func (e *SSHError) Unwrap() error {
return e.Cause
}
