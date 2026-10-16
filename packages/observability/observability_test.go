package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestFailureClassificationPreservesStableMetadata(t *testing.T) {
	failure := Describe(Wrap(errors.New("secret path /private/repository"), contracts.FailureAdapter, "adapter_observe", true))
	if failure.Category != contracts.FailureAdapter || failure.Code != "adapter_observe" || !failure.Retryable {
		t.Fatalf("unexpected failure %#v", failure)
	}
	deadline := Describe(context.DeadlineExceeded)
	if deadline.Category != contracts.FailureTimeout || deadline.Code != "deadline_exceeded" || !deadline.Retryable {
		t.Fatalf("unexpected deadline failure %#v", deadline)
	}
}

func TestJSONLoggerOnlyEmitsAllowlistedFields(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONLogger(&output, "server", "0.5.0")
	err := logger.Record(context.Background(), Event{
		ObservedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Severity:   SeverityError, Operation: "session.prepare", HTTPStatus: 503,
		Status: contracts.StatusUnavailable, FailureCategory: contracts.FailureObjectStore,
		ErrorCode: "artifact_store_unavailable", Retryable: true, DurationMS: 12,
		CorrelationID: "corr_fixture123",
	})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"schema_version", "observed_at", "severity", "component", "version", "operation", "status", "failure_category", "error_code", "retryable", "duration_ms", "correlation_id"} {
		if _, exists := event[required]; !exists {
			t.Fatalf("missing %s in %#v", required, event)
		}
	}
	for _, forbidden := range []string{"message", "path", "request", "response", "authorization", "project_id", "session_id", "account_id"} {
		if _, exists := event[forbidden]; exists {
			t.Fatalf("forbidden field %s in %#v", forbidden, event)
		}
	}
}

func TestEventRejectsFreeFormValues(t *testing.T) {
	_, err := PrepareEvent(Event{Operation: "sync /private/repository", Status: contracts.StatusOK}, "runtime", "dev", time.Now())
	if err == nil {
		t.Fatal("free-form operation was accepted")
	}
}
