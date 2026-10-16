package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

const EventSchemaVersion = 1

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Event struct {
	SchemaVersion   int                       `json:"schema_version"`
	ObservedAt      string                    `json:"observed_at"`
	Severity        Severity                  `json:"severity"`
	Component       string                    `json:"component"`
	Version         string                    `json:"version"`
	Operation       string                    `json:"operation"`
	HTTPStatus      int                       `json:"http_status,omitempty"`
	Status          contracts.Status          `json:"status"`
	FailureCategory contracts.FailureCategory `json:"failure_category,omitempty"`
	ErrorCode       string                    `json:"error_code,omitempty"`
	Retryable       bool                      `json:"retryable"`
	DurationMS      int64                     `json:"duration_ms"`
	CorrelationID   string                    `json:"correlation_id,omitempty"`
}

type Failure struct {
	Category      contracts.FailureCategory
	Code          string
	Retryable     bool
	CorrelationID string
}

// Recorder is shared by structured log sinks. Callers choose persistence and
// retention; logging never owns business retries or task state.
type Recorder interface {
	Record(context.Context, Event) error
}

// RecordBestEffort keeps sink failures outside business transactions.
func RecordBestEffort(ctx context.Context, recorder Recorder, event Event) {
	if recorder != nil {
		_ = recorder.Record(ctx, event)
	}
}

type JSONLogger struct {
	component string
	version   string
	output    io.Writer
	now       func() time.Time
	mutex     sync.Mutex
}

type classifiedError struct {
	error
	category  contracts.FailureCategory
	code      string
	retryable bool
}

type categoryProvider interface {
	FailureCategory() contracts.FailureCategory
}

type codeProvider interface {
	FailureCode() string
}

type retryableProvider interface {
	FailureRetryable() bool
}

type correlationProvider interface {
	FailureCorrelationID() string
}

var (
	eventTokenPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,127}$`)
	correlationPattern = regexp.MustCompile(`^corr_[A-Za-z0-9_-]{8,}$`)
)

func NewJSONLogger(output io.Writer, component, version string) *JSONLogger {
	return &JSONLogger{output: output, component: component, version: version, now: time.Now}
}

func (logger *JSONLogger) Record(_ context.Context, event Event) error {
	if logger == nil || logger.output == nil {
		return nil
	}
	prepared, err := PrepareEvent(event, logger.component, logger.version, logger.now())
	if err != nil {
		return err
	}
	logger.mutex.Lock()
	defer logger.mutex.Unlock()
	return json.NewEncoder(logger.output).Encode(prepared)
}

func PrepareEvent(event Event, component, version string, now time.Time) (Event, error) {
	if event.SchemaVersion == 0 {
		event.SchemaVersion = EventSchemaVersion
	}
	if event.ObservedAt == "" {
		event.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	}
	if event.Component == "" {
		event.Component = component
	}
	if event.Version == "" {
		event.Version = version
	}
	if event.Severity == "" {
		event.Severity = SeverityInfo
	}
	if err := ValidateEvent(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func ValidateEvent(event Event) error {
	if event.SchemaVersion != EventSchemaVersion {
		return errors.New("unsupported observability event schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.ObservedAt); err != nil {
		return errors.New("observability event timestamp is invalid")
	}
	if event.Severity != SeverityInfo && event.Severity != SeverityWarning && event.Severity != SeverityError {
		return errors.New("observability event severity is invalid")
	}
	for name, value := range map[string]string{"component": event.Component, "version": event.Version, "operation": event.Operation} {
		if !eventTokenPattern.MatchString(value) {
			return fmt.Errorf("observability event %s is invalid", name)
		}
	}
	if !validStatus(event.Status) {
		return errors.New("observability event status is invalid")
	}
	if event.FailureCategory != "" && !validCategory(event.FailureCategory) {
		return errors.New("observability event failure category is invalid")
	}
	if event.ErrorCode != "" && !eventTokenPattern.MatchString(event.ErrorCode) {
		return errors.New("observability event error code is invalid")
	}
	if event.CorrelationID != "" && !correlationPattern.MatchString(event.CorrelationID) {
		return errors.New("observability event correlation ID is invalid")
	}
	if event.HTTPStatus < 0 || event.HTTPStatus > 599 || event.DurationMS < 0 {
		return errors.New("observability event numeric field is invalid")
	}
	return nil
}

func Wrap(err error, category contracts.FailureCategory, code string, retryable bool) error {
	if err == nil {
		return nil
	}
	return &classifiedError{error: err, category: category, code: code, retryable: retryable}
}

func (err *classifiedError) Unwrap() error { return err.error }

func (err *classifiedError) FailureCategory() contracts.FailureCategory { return err.category }

func (err *classifiedError) FailureCode() string { return err.code }

func (err *classifiedError) FailureRetryable() bool { return err.retryable }

func Describe(err error) Failure {
	if err == nil {
		return Failure{}
	}
	var categoryValue categoryProvider
	if errors.As(err, &categoryValue) {
		failure := Failure{Category: categoryValue.FailureCategory()}
		var codeValue codeProvider
		if errors.As(err, &codeValue) && eventTokenPattern.MatchString(codeValue.FailureCode()) {
			failure.Code = codeValue.FailureCode()
		}
		var retryableValue retryableProvider
		if errors.As(err, &retryableValue) {
			failure.Retryable = retryableValue.FailureRetryable()
		}
		var correlationValue correlationProvider
		if errors.As(err, &correlationValue) && correlationPattern.MatchString(correlationValue.FailureCorrelationID()) {
			failure.CorrelationID = correlationValue.FailureCorrelationID()
		}
		if failure.Category != "" && failure.Code == "" {
			failure.Code = defaultCode(failure.Category)
		}
		return failure
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Failure{Category: contracts.FailureTimeout, Code: "deadline_exceeded", Retryable: true}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return Failure{Category: contracts.FailureTimeout, Code: "network_timeout", Retryable: true}
		}
		return Failure{Category: contracts.FailureNetwork, Code: "network_error", Retryable: true}
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return Failure{Category: contracts.FailureLocalState, Code: "local_state_io", Retryable: false}
	}
	return Failure{Category: contracts.FailureServerInternal, Code: "internal_error", Retryable: true}
}

func CategoryForResponse(status contracts.Status, code string, httpStatus int) contracts.FailureCategory {
	if httpStatus == 401 || code == "registration_token_required" {
		return contracts.FailureAuthentication
	}
	if httpStatus == 403 || status == contracts.StatusForbidden {
		return contracts.FailureAuthorization
	}
	if status == contracts.StatusIntegrityError {
		return contracts.FailureArtifactIntegrity
	}
	if status == contracts.StatusUnavailable && objectStoreCode(code) {
		return contracts.FailureObjectStore
	}
	if status == contracts.StatusUnavailable || status == contracts.StatusInternalError || httpStatus >= 500 {
		return contracts.FailureServerInternal
	}
	return ""
}

func StatusForFailure(category contracts.FailureCategory) contracts.Status {
	switch category {
	case contracts.FailureAuthentication, contracts.FailureAuthorization:
		return contracts.StatusForbidden
	case contracts.FailureArtifactIntegrity:
		return contracts.StatusIntegrityError
	case contracts.FailureServerInternal:
		return contracts.StatusInternalError
	default:
		return contracts.StatusUnavailable
	}
}

func validCategory(category contracts.FailureCategory) bool {
	switch category {
	case contracts.FailureNetwork, contracts.FailureTimeout, contracts.FailureDeploymentIdentity,
		contracts.FailureAuthentication, contracts.FailureAuthorization, contracts.FailureAdapter,
		contracts.FailureLocalState, contracts.FailureArtifactIntegrity, contracts.FailureObjectStore,
		contracts.FailureServerInternal:
		return true
	default:
		return false
	}
}

func validStatus(status contracts.Status) bool {
	switch status {
	case contracts.StatusOK, contracts.StatusPending, contracts.StatusDegraded, contracts.StatusUnavailable,
		contracts.StatusNotConnected, contracts.StatusForbidden, contracts.StatusProjectArchived,
		contracts.StatusNotFound, contracts.StatusUnsupported, contracts.StatusInvalidRequest,
		contracts.StatusIntegrityError, contracts.StatusInternalError:
		return true
	default:
		return false
	}
}

func defaultCode(category contracts.FailureCategory) string {
	return string(category) + "_error"
}

func objectStoreCode(code string) bool {
	switch code {
	case "artifact_store_unavailable", "upload_authorization_failed", "download_authorization_failed", "artifact_read_failed":
		return true
	default:
		return false
	}
}
