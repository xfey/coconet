package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func TestFailureExplanationsAreSafeAndDistinguishRepair(t *testing.T) {
	cases := []struct {
		code     string
		category contracts.FailureCategory
		want     string
	}{
		{"work_extraction_http_401", contracts.FailureAuthentication, "模型接入"},
		{"library_tags_http_429", contracts.FailureServerInternal, "配额"},
		{"graph_action_invalid_output", contracts.FailureServerInternal, "校验"},
		{"history_bootstrap_input_too_large", contracts.FailureServerInternal, "容量"},
		{"history_patch_http_400", contracts.FailureServerInternal, "拒绝"},
		{"library_checkpoint_needs_context", contracts.FailureServerInternal, "证据不足"},
		{"library_tags_source_invalid", contracts.FailureServerInternal, "来源"},
		{"network_error", contracts.FailureNetwork, "网络"},
		{"deadline_exceeded", contracts.FailureTimeout, "超时"},
		{"authentication_error", contracts.FailureAuthentication, "身份凭据"},
		{"private /path token=secret", "", "服务未能"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			explanation := Explain(Failure{Code: tc.code, Category: tc.category})
			if !strings.Contains(explanation.Reason, tc.want) || explanation.Action == "" || strings.Contains(explanation.Reason+explanation.Action, "secret") {
				t.Fatal(explanation)
			}
		})
	}
}

func TestAggregateFailuresCountsRetainedEventsAndLatestCorrelation(t *testing.T) {
	event, err := PrepareEvent(Event{Operation: "sync.process", Status: contracts.StatusUnavailable, FailureCategory: contracts.FailureTimeout, ErrorCode: "deadline_exceeded", Retryable: true, CorrelationID: "corr_first000"}, "runtime", "test", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	later := event
	later.ObservedAt = "2026-01-01T00:00:00.1Z"
	later.CorrelationID = "corr_later000"
	distinct := later
	distinct.Retryable = false
	invalid := later
	invalid.ErrorCode = "secret body /path"
	info := event
	info.FailureCategory = ""
	info.ErrorCode = ""
	got := AggregateFailures([]Event{later, event, distinct, invalid, info})
	if len(got) != 2 {
		t.Fatal(got)
	}
	for _, item := range got {
		if item.CorrelationID != later.CorrelationID || item.Explanation == nil {
			t.Fatal(item)
		}
		if item.Retryable && item.EventCount != 2 {
			t.Fatal(item)
		}
		if !item.Retryable && item.EventCount != 1 {
			t.Fatal(item)
		}
	}
}

type failedRecorder struct{ calls int }

func (s *failedRecorder) Record(context.Context, Event) error {
	s.calls++
	return errors.New("sink unavailable")
}
func TestRecordBestEffortIgnoresSinkFailure(t *testing.T) {
	sink := &failedRecorder{}
	RecordBestEffort(context.Background(), sink, Event{})
	RecordBestEffort(context.Background(), nil, Event{})
	if sink.calls != 1 {
		t.Fatal(sink.calls)
	}
}

func TestProjectionInputFailureExplainsPreparationRatherThanModelOutput(t *testing.T) {
	for _, code := range []string{"history_patch_input_invalid", "bootstrap_input_invalid"} {
		explanation := Explain(Failure{Code: code})
		if !strings.Contains(explanation.Reason, "尚未进入模型推理") || strings.Contains(explanation.Reason, "模型结果未通过") {
			t.Fatal(code, explanation)
		}
	}
}
