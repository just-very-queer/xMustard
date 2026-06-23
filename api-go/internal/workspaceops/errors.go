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
	for _, s := range []error{
		errNoRoot, errEmptyPath, errAbsPath, errEscape, errNotRegular,
		// existing per-subsystem validation sentinels — all client-input errors,
		// so the central mapper classifies them 400 without each handler's substring
		// check (XM-PRO-013).
		errInvalidPostgresRequest, ErrInvalidSemanticRequest, ErrInvalidDiagnosticsRequest,
	} {
		if errors.Is(err, s) {
			return true
		}
	}
	if de, ok := AsDomainError(err); ok {
		return de.Class == ClassInvalidInput
	}
	return false
}

// ErrorClass is the stable, wording-independent category of a domain error. The HTTP
// layer maps it to a status code, so re-phrasing an error message can no longer flip
// 400/404/409/503/500 the way the old strings.Contains(err.Error(), ...) classifiers
// did (XM-PRO-013).
type ErrorClass int

const (
	ClassInternal     ErrorClass = iota // 500 — unexpected; the wrapped detail is NOT exposed
	ClassInvalidInput                   // 400 — bad client input
	ClassNotFound                       // 404
	ClassConflict                       // 409 — state conflict (already exists, wrong phase)
	ClassUnavailable                    // 503 — a dependency/runtime is not configured/ready
)

// DomainError carries a typed class + a client-safe public message, decoupling HTTP
// status (and what is disclosed) from the human-readable wording. Construct via
// Invalid/NotFoundErr/Conflict/Unavailable; wrap an internal cause with WithCause.
type DomainError struct {
	Class  ErrorClass
	Public string // safe to return to the client
	cause  error  // internal detail: logged, exposed only for non-Internal classes
}

func (e *DomainError) Error() string {
	if e.cause != nil {
		return e.Public + ": " + e.cause.Error()
	}
	return e.Public
}

func (e *DomainError) Unwrap() error { return e.cause }

// WithCause attaches an internal cause (preserved for logs / errors.Is chains).
func (e *DomainError) WithCause(cause error) *DomainError { e.cause = cause; return e }

func Invalid(public string) *DomainError {
	return &DomainError{Class: ClassInvalidInput, Public: public}
}
func NotFoundErr(public string) *DomainError {
	return &DomainError{Class: ClassNotFound, Public: public}
}
func Conflict(public string) *DomainError { return &DomainError{Class: ClassConflict, Public: public} }
func Unavailable(public string) *DomainError {
	return &DomainError{Class: ClassUnavailable, Public: public}
}

// AsDomainError unwraps err to a *DomainError if one is in its chain.
func AsDomainError(err error) (*DomainError, bool) {
	var de *DomainError
	if errors.As(err, &de) {
		return de, true
	}
	return nil, false
}
