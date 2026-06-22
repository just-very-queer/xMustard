package workspaceops

import "errors"

// ErrInvalidInput marks an error caused by bad CLIENT input — a path that escapes
// the workspace, a malformed id, a missing/empty required field, an unparseable
// argument. The HTTP layer maps it to 400, not 500, so a malformed request is not
// reported as a server fault (which misleads monitoring and any client that retries
// 5xx but not 4xx). Wrap a validation error with it via fmt.Errorf("...: %w", ErrInvalidInput).
var ErrInvalidInput = errors.New("invalid input")

// IsInvalidInput reports whether err is (or wraps) a client-input validation
// failure. It recognizes both ErrInvalidInput-wrapped errors and the path-
// confinement sentinels from safepath.go (which are returned by identity), so the
// HTTP layer can classify them as 400 without each call site re-wrapping.
func IsInvalidInput(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrInvalidInput) {
		return true
	}
	for _, s := range []error{errNoRoot, errEmptyPath, errAbsPath, errEscape, errNotRegular} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}
