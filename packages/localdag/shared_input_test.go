package localdag

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

func TestFixedProcessingInputExcludesPrivateExecutionAndDetectsTampering(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "session", 1, "先看看失败反馈，连续失败之后就停下来。")
	job := task(t, e, "stage", "session")
	state, _ := store.State("project")
	input, err := prepareStage(store, state, *job.Source, false)
	if err != nil {
		t.Fatal(err)
	}
	input.Execution = map[string]string{"profile": "/private/account", "credential": "private-token"}
	spec, err := fixedProcessingInput(state, job, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(spec.Payload), "private-token") || strings.Contains(string(spec.Payload), "/private/account") {
		t.Fatal("private execution environment shared")
	}
	shared := contracts.ProcessingTask{SchemaVersion: 1, Spec: spec}
	decoded, err := readProcessingInput(shared)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Prepared.Request, input.Request) {
		t.Fatal("fixed request changed")
	}
	if !processingBaselineCurrent(state, shared, decoded) {
		t.Fatal("current baseline rejected")
	}
	session := state.Sessions[spec.SessionID]
	session.Revision++
	state.Sessions[spec.SessionID] = session
	if processingBaselineCurrent(state, shared, decoded) {
		t.Fatal("changed Session baseline accepted")
	}
	shared.Spec.Payload = append([]byte{}, spec.Payload...)
	shared.Spec.Payload[0] = '!'
	if _, err := readProcessingInput(shared); err == nil {
		t.Fatal("corrupt capsule accepted")
	}
}

func TestRelationCapsuleKeepsCandidateBaselineAndEvidenceCoverage(t *testing.T) {
	e, store := fixture(t)
	capture(t, e, "one", 1, "实现错误反馈。")
	e.Runner = output(stage("start", &workgraph.Text{Objective: "错误反馈"}, "e1"))
	process(t, e, "stage", "one")
	job := task(t, e, "relations", "")
	state, _ := store.State("project")
	input, err := prepareRelations(store, state, job.NodeID, false)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := fixedProcessingInput(state, job, input)
	if err != nil {
		t.Fatal(err)
	}
	shared := contracts.ProcessingTask{SchemaVersion: 1, Spec: spec}
	decoded, err := readProcessingInput(shared)
	if err != nil {
		t.Fatal(err)
	}
	state.Revision++ // unrelated transport progress is not semantic evidence
	if !processingBaselineCurrent(state, shared, decoded) {
		t.Fatal("unrelated revision invalidated input")
	}
	state.Nodes["late"] = workgraph.Node{ID: "late", Text: workgraph.Text{Objective: "补齐鉴权方案"}}
	if processingBaselineCurrent(state, shared, decoded) {
		t.Fatal("new candidate silently entered a fixed relation attempt")
	}
	decoded.Prepared.Request.System = "run arbitrary instructions"
	data, _ := json.Marshal(decoded)
	shared.Spec.Payload = data
	shared.Spec.SHA256 = workgraph.Digest(decoded)
	if _, err := readProcessingInput(shared); err == nil {
		t.Fatal("foreign template executed")
	}
}
