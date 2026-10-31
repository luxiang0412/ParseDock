// Package jobs defines the job model, the two-dimensional lifecycle
// (processing + retention) and the public request/response DTOs.
package jobs

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ProcessingStatus is the first lifecycle dimension.
type ProcessingStatus string

const (
	StatusQueued    ProcessingStatus = "queued"
	StatusRunning   ProcessingStatus = "running"
	StatusCompleted ProcessingStatus = "completed"
	StatusFailed    ProcessingStatus = "failed"
	StatusCancelled ProcessingStatus = "cancelled"
)

func (s ProcessingStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// RetentionStatus is the second lifecycle dimension. It is independent of
// processing status: a running job has retention "active" too.
type RetentionStatus string

const (
	RetentionActive         RetentionStatus = "active"
	RetentionReleasePending RetentionStatus = "release_pending"
	RetentionExpiryPending  RetentionStatus = "expiry_pending"
	RetentionDeleting       RetentionStatus = "deleting"
	RetentionReleased       RetentionStatus = "released"
	RetentionExpired        RetentionStatus = "expired"
)

func (s RetentionStatus) Terminal() bool {
	return s == RetentionReleased || s == RetentionExpired
}

// Mode and device requests. These are the public knobs; the resolved engine is
// recorded in the result metadata rather than being user-selectable.
const (
	ModeAuto     = "auto"
	ModeFast     = "fast"
	ModeAccurate = "accurate"

	DeviceAuto         = "auto"
	DeviceCPU          = "cpu"
	DeviceGPUPreferred = "gpu_preferred"
	DeviceGPURequired  = "gpu_required"
)

// Stable machine-readable error codes.
const (
	ErrInvalidRequest    = "invalid_request"
	ErrUnsupportedType   = "unsupported_type"
	ErrTooLarge          = "file_too_large"
	ErrMalformed         = "malformed_file"
	ErrEncrypted         = "encrypted_file"
	ErrPageLimit         = "page_limit_exceeded"
	ErrZipBomb           = "suspicious_compression"
	ErrExpandedTooLarge  = "expanded_too_large"
	ErrEngineOOM         = "engine_out_of_memory"
	ErrQuotaExceeded     = "quota_exceeded"
	ErrInsufficientSpace = "insufficient_storage"
	ErrEngineUnavailable = "engine_unavailable"
	ErrGPUUnavailable    = "gpu_unavailable"
	ErrTimeout           = "timeout"
	ErrCancelled         = "cancelled"
	ErrInternal          = "internal_error"
	ErrLeaseLost         = "lease_lost"
	ErrNotFound          = "not_found"
	ErrGone              = "gone"
	ErrConflict          = "conflict"
	ErrUnauthorized      = "unauthorized"
	ErrRateLimited       = "rate_limited"
)

// Job is the persisted aggregate.
type Job struct {
	ID                 string           `json:"job_id"`
	OwnerID            string           `json:"-"`
	InputName          string           `json:"input_name"`
	MimeType           string           `json:"mime_type"`
	Format             string           `json:"format"`
	InputSHA256        string           `json:"input_sha256"`
	InputSize          int64            `json:"input_size"`
	PageCount          int              `json:"page_count"`
	ProcessingStatus   ProcessingStatus `json:"status"`
	RetentionStatus    RetentionStatus  `json:"retention_status"`
	Priority           int              `json:"-"`
	RequestedMode      string           `json:"requested_mode"`
	RequestedDevice    string           `json:"requested_device"`
	EngineHint         string           `json:"engine_hint"`
	ActualEngine       string           `json:"engine,omitempty"`
	RoutingReason      string           `json:"routing_reason,omitempty"`
	IdempotencyKey     string           `json:"-"`
	CreatedAt          time.Time        `json:"created_at"`
	StartedAt          *time.Time       `json:"started_at,omitempty"`
	CompletedAt        *time.Time       `json:"completed_at,omitempty"`
	ResultExpiresAt    *time.Time       `json:"result_expires_at,omitempty"`
	MaxLifetimeAt      time.Time        `json:"max_lifetime_at"`
	CurrentAttempt     int              `json:"attempt"`
	MaxAttempts        int              `json:"max_attempts"`
	ResultVersion      string           `json:"result_version,omitempty"`
	ResultManifestPath string           `json:"-"`
	ErrorCode          string           `json:"error_code,omitempty"`
	ErrorMessage       string           `json:"error_message,omitempty"`
	WarningCount       int              `json:"warning_count"`
	TombstoneExpiresAt *time.Time       `json:"-"`
	RequestID          string           `json:"-"`
	CancelRequestedAt  *time.Time       `json:"cancel_requested_at,omitempty"`
	Progress           int              `json:"progress_percent"`
	ProgressStage      string           `json:"progress_stage,omitempty"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// ResultAvailable reports whether a downloadable result currently exists.
func (j *Job) ResultAvailable() bool {
	return j.ProcessingStatus == StatusCompleted && j.ResultVersion != "" &&
		(j.RetentionStatus == RetentionActive || j.RetentionStatus == RetentionReleasePending ||
			j.RetentionStatus == RetentionExpiryPending || j.RetentionStatus == RetentionDeleting)
}

// Gone reports whether retention says the artifact is (being) removed.
func (j *Job) Gone() bool {
	switch j.RetentionStatus {
	case RetentionReleasePending, RetentionExpiryPending, RetentionDeleting, RetentionReleased, RetentionExpired:
		return true
	}
	return false
}

// Attempt is one worker execution attempt; lease_token is the fencing token.
type Attempt struct {
	JobID           string     `json:"job_id"`
	AttemptNo       int        `json:"attempt_no"`
	WorkerID        string     `json:"worker_id"`
	LeaseToken      string     `json:"lease_token"`
	LeaseUntil      time.Time  `json:"lease_until"`
	StartedAt       time.Time  `json:"started_at"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	State           string     `json:"state"` // running|succeeded|failed|expired|cancelled
	Error           string     `json:"error,omitempty"`
	DeviceType      string     `json:"device_type,omitempty"`
	Engine          string     `json:"engine,omitempty"`
}

// Attempt states.
const (
	AttemptRunning   = "running"
	AttemptSucceeded = "succeeded"
	AttemptFailed    = "failed"
	AttemptExpired   = "expired"
	AttemptCancelled = "cancelled"
)

// MaxIdempotencyKeyLen is the longest accepted Idempotency-Key, in characters
// (api/openapi.yaml: maxLength 200).
const MaxIdempotencyKeyLen = 200

// NormalizeIdempotencyKey trims surrounding whitespace and rejects keys longer than
// MaxIdempotencyKeyLen characters. Truncating instead would make distinct long keys that
// share a 200-character prefix collide, replaying one client operation as another.
func NormalizeIdempotencyKey(k string) (string, error) {
	k = strings.TrimSpace(k)
	if utf8.RuneCountInString(k) > MaxIdempotencyKeyLen {
		return "", fmt.Errorf("idempotency key must be at most %d characters", MaxIdempotencyKeyLen)
	}
	return k, nil
}

var b32 = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// NewID returns a ULID-shaped identifier: 48-bit millis + 80 bits randomness,
// Crockford base32. Monotonicity is not guaranteed across processes, which is
// acceptable because IDs are opaque.
func NewID(prefix string) string {
	var randPart [10]byte
	if _, err := rand.Read(randPart[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	ms := uint64(time.Now().UnixMilli())
	var b [16]byte
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	copy(b[6:], randPart[:])
	return prefix + b32.EncodeToString(b[:])
}

// NewJobID / NewLeaseID / NewEventID helpers.
func NewJobID() string    { return NewID("job_") }
func NewLeaseID() string  { return NewID("dl_") }
func NewToken() string    { return NewID("lt_") }
func NewWorkerID() string { return NewID("wrk_") }

// ValidateMode normalizes mode.
func ValidateMode(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeFast:
		return ModeFast, nil
	case ModeAccurate:
		return ModeAccurate, nil
	}
	return "", fmt.Errorf("mode must be one of auto|fast|accurate")
}

// ValidateDevice normalizes device.
func ValidateDevice(d string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "", DeviceAuto:
		return DeviceAuto, nil
	case DeviceCPU:
		return DeviceCPU, nil
	case DeviceGPUPreferred:
		return DeviceGPUPreferred, nil
	case DeviceGPURequired:
		return DeviceGPURequired, nil
	}
	return "", errors.New("device must be one of auto|cpu|gpu_preferred|gpu_required")
}
