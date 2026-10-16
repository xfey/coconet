package inference

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCursorInferenceProcessFixture(t *testing.T) {
	mode := os.Getenv("COCO_CURSOR_FIXTURE")
	if mode == "" {
		return
	}
	pos := slices.Index(os.Args, "--allowed-tools")
	if pos < 0 || pos+1 >= len(os.Args) || os.Args[pos+1] != "" || !slices.Contains(os.Args, "--disable-project-configs") || !slices.Contains(os.Args, "--trust") || slices.Contains(os.Args, "--exclude-workspace-context") {
		os.Exit(21)
	}
	if os.Getenv("COCONET_INTERNAL_INFERENCE") != "1" || os.Getenv("CURSOR_AGENT_PERSIST_SESSION") != "" || os.Getenv("CURSOR_CONVERSATION_ID") != "" || os.Getenv("CURSOR_AGENT") != "" {
		os.Exit(22)
	}
	b, _ := io.ReadAll(os.Stdin)
	if !strings.Contains(string(b), "OUTPUT_SCHEMA") || !strings.Contains(string(b), "fixture request") {
		os.Exit(23)
	}
	cwd, _ := os.Getwd()
	sum := md5.Sum([]byte(cwd))
	dir := filepath.Join(os.Getenv("CURSOR_CONFIG_DIR"), "chats", hex.EncodeToString(sum[:]))
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "owned.db"), []byte("fixture"), 0600)
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "secret=never-log-me Authentication required")
		os.Exit(1)
	}
	if mode == "quota-stderr" || mode == "quota-stdout" || mode == "quota-envelope" {
		message := "secret=never-log-me You've hit your free requests limit. limitType: included_usage"
		switch mode {
		case "quota-stderr":
			fmt.Fprintln(os.Stderr, message)
		case "quota-stdout":
			fmt.Println(message)
		case "quota-envelope":
			json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "error", "is_error": true, "result": message})
			os.Exit(0)
		}
		os.Exit(1)
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if mode == "bad" {
		fmt.Print("not JSON")
		os.Exit(0)
	}
	if mode == "default" && slices.Contains(os.Args, "--model") {
		os.Exit(24)
	}
	if mode == "explicit" {
		i := slices.Index(os.Args, "--model")
		if i < 0 || os.Args[i+1] != "test-model" {
			os.Exit(25)
		}
	}
	result := `{"ok":true}`
	if mode == "fenced" {
		result = "```json\n" + result + "```"
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "success", "result": result, "usage": map[string]int{"inputTokens": 20}})
	os.Exit(0)
}

func TestCursorInferenceIsolationAndFailures(t *testing.T) {
	for _, mode := range []string{"default", "explicit", "fenced", "fail", "bad", "hang", "quota-stderr", "quota-stdout", "quota-envelope"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CURSOR_CONFIG_DIR", "/ignored")
			t.Setenv("CURSOR_AGENT_PERSIST_SESSION", "1")
			t.Setenv("CURSOR_CONVERSATION_ID", "old-chat")
			t.Setenv("CURSOR_AGENT", "1")
			root := t.TempDir()
			work := t.TempDir()
			unrelated := filepath.Join(root, "chats", "unrelated")
			os.MkdirAll(unrelated, 0700)
			os.WriteFile(filepath.Join(unrelated, "keep"), []byte("untouched"), 0600)
			config := Config{Provider: "cursor", Command: os.Args[0], Args: []string{"-test.run=^TestCursorInferenceProcessFixture$", "--"}, ConfigRoot: root, WorkRoot: work, Timeout: 2 * time.Second, Env: []string{"COCO_CURSOR_FIXTURE=" + mode}}
			if mode == "explicit" {
				config.Model = "test-model"
			}
			if mode == "hang" {
				config.Timeout = 100 * time.Millisecond
			}
			response, err := (Local{Config: config}).Infer(context.Background(), Request{System: "fixture request", Input: json.RawMessage(`{"topic":"queue"}`), Schema: json.RawMessage(`{"type":"object"}`)})
			wantFail := mode == "fail" || mode == "bad" || mode == "hang" || strings.HasPrefix(mode, "quota-")
			if (err != nil) != wantFail {
				t.Fatal(response, err)
			}
			if !wantFail && response.Text != `{"ok":true}` {
				t.Fatal(response)
			}
			if mode == "fenced" && (!strings.Contains(response.Visible, "```json") || !strings.Contains(string(response.Usage), `"inputTokens":20`)) {
				t.Fatal("lost original envelope or usage", response)
			}
			if err != nil && strings.Contains(err.Error(), "never-log-me") {
				t.Fatal("leaked provider diagnostics")
			}
			if strings.HasPrefix(mode, "quota-") && (err == nil || !strings.Contains(err.Error(), "quota_exhausted: restore Cursor account usage")) {
				t.Fatalf("quota failure needs account guidance: %v", err)
			}
			entries, _ := os.ReadDir(work)
			if len(entries) != 0 {
				t.Fatal("inference work leaked")
			}
			entries, _ = os.ReadDir(filepath.Join(root, "chats"))
			if len(entries) != 1 || entries[0].Name() != "unrelated" {
				t.Fatal("native capture bucket leaked or unrelated history removed")
			}
		})
	}
}

func TestCursorJSONCodeBlockBoundary(t *testing.T) {
	for _, raw := range []string{"```json\n{\"ok\":true}\n```", "```json\n{\"ok\":true}```", " \r\n```\r\n{\"ok\":true}\r\n``` \r\n"} {
		if got := unwrapCursorJSON(raw); got != `{"ok":true}` {
			t.Fatalf("complete JSON block not decoded: %q", got)
		}
	}
	for _, raw := range []string{
		`{"ok":true}`, "Explanation\n```json\n{}\n```", "```json\n{}\n```\nMore text",
		"```json\n{}\n```\n```json\n{}\n```", "```json\n{} {}\n```", "```json\n{\n```",
		"```json\n{}", "```javascript\n{}\n```", "```json{} ```",
	} {
		if got := unwrapCursorJSON(raw); got != raw {
			t.Fatalf("rewrote bare, ambiguous or malformed output: %q -> %q", raw, got)
		}
	}
}

func TestCursorInferenceCopiesOnlyModelAndConnectionPreferences(t *testing.T) {
	source, work := t.TempDir(), t.TempDir()
	original := []byte(`{"model":"auto","network":{"useHttp1ForAgent":true},"authInfo":{"teamId":123},"permissions":{"allow":["Shell(*)"]},"hooks":{"stop":[]},"unknown":"keep at source"}`)
	path := filepath.Join(source, "cli-config.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := cursorInferenceProfile(source, work)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(profile, "cli-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if string(config["model"]) != `"auto"` || config["network"] == nil || config["authInfo"] == nil || config["hooks"] != nil || config["unknown"] != nil || string(config["permissions"]) != `{"allow":[],"deny":[]}` {
		t.Fatal(string(data))
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("source preferences changed")
	}
	for _, invalid := range []string{`null`, `[]`, `{`} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := cursorInferenceProfile(source, t.TempDir()); err == nil {
			t.Fatal("accepted invalid settings", invalid)
		}
	}
}
