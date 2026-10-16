package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xfey/coconet/adapters"
)

func TestHelperForkRemapsOnlyCopyMetadataAndPreservesNewerLocalHistory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	profile, cwd := t.TempDir(), t.TempDir()
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := "01980000-0000-7000-8000-000000000001"
	sourceCWD := "/different-device/original-workspace"
	nested := map[string]any{"sessionId": sourceID, "cwd": sourceCWD, "text": "literal source text"}
	record := map[string]any{"sessionId": sourceID, "cwd": sourceCWD, "type": "user", "uuid": "original-message-uuid", "parentUuid": nil, "message": nested}
	fixed, _ := json.Marshal(record)
	fixed = append(fixed, '\n')
	source := filepath.Join(t.TempDir(), "fixed.jsonl")
	if err := os.WriteFile(source, fixed, 0600); err != nil {
		t.Fatal(err)
	}
	encoded := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, cwd)
	project := filepath.Join(profile, "projects", encoded)
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(project, sourceID+".jsonl")
	latest := append(bytes.Clone(fixed), []byte("newer native history must remain untouched\n")...)
	if err := os.WriteFile(live, latest, 0600); err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(t.TempDir(), "sdk.mjs")
	code := `import {readFile,writeFile,access,unlink} from 'node:fs/promises';
import {join} from 'node:path';
const path=(id,dir)=>join(process.env.CLAUDE_CONFIG_DIR,'projects',dir.replace(/[^a-zA-Z0-9]/g,'-'),id+'.jsonl');
export async function getSessionMessages(id,{dir}){return (await readFile(path(id,dir),'utf8')).trim().split('\n').map(JSON.parse);}
export async function getSessionInfo(id,{dir}){await access(path(id,dir));try{await unlink(join(process.env.CLAUDE_CONFIG_DIR,'fail-verify'));throw Error('injected verification failure');}catch(error){if(error.code!=='ENOENT')throw error;}return {sessionId:id,cwd:(await getSessionMessages(id,{dir}))[0].cwd};}
export async function forkSession(id,{dir}){
 const records=await getSessionMessages(id,{dir});
 if(id==='01980000-0000-7000-8000-000000000001'||records[0].sessionId!==id)throw Error('source was not independently imported');
 const child='01980000-0000-7000-8000-000000000002';
 await writeFile(path(child,dir),records.map(r=>JSON.stringify({...r,sessionId:child})).join('\n')+'\n',{flag:'wx'});
 return {sessionId:child};
}`
	if err := os.WriteFile(sdk, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCONET_CLAUDE_SDK_ENTRY", sdk)
	adapter := New("tclaude")
	adapter.ConfigRoot = profile
	adapter.NodeCommand = node
	adapter.HelperPath, err = filepath.Abs(filepath.Join("helper", "helper.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	hash, _, err := adapters.HashFile(source)
	if err != nil {
		t.Fatal(err)
	}
	request := adapters.ForkRequest{AttemptDirectory: t.TempDir(), SourceArtifactPath: source, SourceArtifactSHA256: hash, SourceNativeSessionID: sourceID, CWD: cwd}
	if err := os.WriteFile(filepath.Join(profile, "fail-verify"), []byte("once"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Fork(context.Background(), request); err == nil {
		t.Fatal("injected native verification failure disappeared")
	}
	first, err := adapter.Fork(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := adapter.Fork(context.Background(), request)
	if err != nil || again != first {
		t.Fatal("marker did not preserve child", again, err)
	}
	for path, want := range map[string][]byte{source: fixed, live: latest} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("original or fixed artifact modified", err)
		}
	}
	child, err := os.ReadFile(filepath.Join(project, first.NativeSessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(child), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["uuid"] != "original-message-uuid" || parsed["message"].(map[string]any)["sessionId"] != sourceID {
		t.Fatal("non-routing payload was rewritten", parsed)
	}
	if parsed["cwd"] != cwd || parsed["message"].(map[string]any)["cwd"] != sourceCWD {
		t.Fatal("Fork must move routing metadata but preserve nested evidence", parsed)
	}
	parsed["cwd"] = sourceCWD
	wrongWorkspace, _ := json.Marshal(parsed)
	if err := os.WriteFile(filepath.Join(project, first.NativeSessionID+".jsonl"), wrongWorkspace, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Fork(context.Background(), request); err == nil || !strings.Contains(err.Error(), "different workspace") {
		t.Fatal("recorded Fork with wrong workspace was accepted", err)
	}
	files, err := os.ReadDir(project)
	if err != nil || len(files) != 2 {
		t.Fatal("temporary native import not removed", files, err)
	}
}
