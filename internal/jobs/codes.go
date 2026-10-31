package jobs

// knownErrorCodes is the set of stable codes declared in job.go. TestKnownErrorCodesMatchConstants
// parses the package source and fails when a code is added there but not here (or vice versa),
// so the two cannot drift silently.
var knownErrorCodes = map[string]bool{
	ErrInvalidRequest:    true,
	ErrUnsupportedType:   true,
	ErrTooLarge:          true,
	ErrMalformed:         true,
	ErrEncrypted:         true,
	ErrPageLimit:         true,
	ErrZipBomb:           true,
	ErrExpandedTooLarge:  true,
	ErrEngineOOM:         true,
	ErrQuotaExceeded:     true,
	ErrInsufficientSpace: true,
	ErrEngineUnavailable: true,
	ErrGPUUnavailable:    true,
	ErrTimeout:           true,
	ErrCancelled:         true,
	ErrInternal:          true,
	ErrLeaseLost:         true,
	ErrNotFound:          true,
	ErrGone:              true,
	ErrConflict:          true,
	ErrUnauthorized:      true,
	ErrRateLimited:       true,
	// Produced only by the API gateway; listed so the set mirrors job.go exactly.
	ErrInsufficientScope:      true,
	ErrCoordinatorUnavailable: true,
}

// IsKnownErrorCode reports whether code is one of the stable error codes. Codes that arrive
// from outside the coordinator (a worker's /fail report) must pass this before they are stored
// and returned to clients, because the code set is a public contract.
func IsKnownErrorCode(code string) bool { return knownErrorCodes[code] }
