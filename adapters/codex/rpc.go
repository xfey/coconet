package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type appServerClient interface {
	Request(string, any, any) error
	Close() error
}

type notificationClient interface {
	WaitNotification(string, string, any) error
}

type rpcNotification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type clientStarter func(context.Context, string, string, string) (appServerClient, error)

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type rpcError struct {
	Method  string
	Code    int
	Message string
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s: %s", e.Method, e.Message) }

type processClient struct {
	command       *exec.Cmd
	stdin         io.WriteCloser
	scanner       *bufio.Scanner
	stderr        bytes.Buffer
	notifications []rpcNotification
	nextID        int
	closed        bool
	mutex         sync.Mutex
}

func startProcessClient(ctx context.Context, command, configRoot, cwd string) (appServerClient, error) {
	process := exec.CommandContext(ctx, command, "app-server")
	process.Dir = cwd
	process.Env = replaceEnvironment(os.Environ(), "CODEX_HOME", configRoot)
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	client := &processClient{command: process, stdin: stdin, nextID: 1}
	client.scanner = bufio.NewScanner(stdout)
	client.scanner.Buffer(make([]byte, 64<<10), 64<<20)
	process.Stderr = &limitedWriter{destination: &client.stderr, remaining: 4000}
	if err := process.Start(); err != nil {
		return nil, err
	}
	if err := client.Request("initialize", map[string]any{"clientInfo": map[string]string{"name": "coconet", "version": adapterVersion}, "capabilities": map[string]bool{"experimentalApi": true}}, &struct{}{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := client.notify("initialized", nil); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (client *processClient) Request(method string, params, target any) error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed {
		return errors.New("Codex App Server client is closed")
	}
	id := client.nextID
	client.nextID++
	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(client.stdin, "%s\n", payload); err != nil {
		return err
	}
	for client.scanner.Scan() {
		var response rpcResponse
		if json.Unmarshal(client.scanner.Bytes(), &response) != nil || response.ID != id {
			client.rememberNotification(client.scanner.Bytes())
			continue
		}
		if response.Error != nil {
			return &rpcError{Method: method, Code: response.Error.Code, Message: response.Error.Message}
		}
		if target == nil {
			return nil
		}
		if err := json.Unmarshal(response.Result, target); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	}
	if err := client.scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%s: Codex App Server exited before responding: %s", method, strings.TrimSpace(client.stderr.String()))
}

// Only completion notifications are retained, with a small bound. Normal
// discovery/observation clients never accumulate unrelated thread events.
func (client *processClient) rememberNotification(data []byte) {
	var notification rpcNotification
	if json.Unmarshal(data, &notification) == nil && notification.Method == "externalAgentConfig/import/completed" && len(client.notifications) < 32 {
		client.notifications = append(client.notifications, notification)
	}
}

func (client *processClient) WaitNotification(method, importID string, target any) error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed {
		return errors.New("Codex App Server client is closed")
	}
	for {
		for i, n := range client.notifications {
			var identity struct {
				ImportID string `json:"importId"`
			}
			if n.Method == method && json.Unmarshal(n.Params, &identity) == nil && identity.ImportID == importID {
				client.notifications = append(client.notifications[:i], client.notifications[i+1:]...)
				return json.Unmarshal(n.Params, target)
			}
		}
		if !client.scanner.Scan() {
			if err := client.scanner.Err(); err != nil {
				return err
			}
			return errors.New("Codex App Server exited before import completion; retry the same Fork request")
		}
		client.rememberNotification(client.scanner.Bytes())
	}
}

func (client *processClient) notify(method string, params any) error {
	payload := map[string]any{"method": method}
	if params != nil {
		payload["params"] = params
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(client.stdin, "%s\n", content)
	return err
}

func (client *processClient) Close() error {
	client.mutex.Lock()
	if client.closed {
		client.mutex.Unlock()
		return nil
	}
	client.closed = true
	_ = client.stdin.Close()
	client.mutex.Unlock()
	done := make(chan error, 1)
	go func() { done <- client.command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = client.command.Process.Kill()
		<-done
		return nil
	}
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

type limitedWriter struct {
	destination *bytes.Buffer
	remaining   int
}

func (writer *limitedWriter) Write(content []byte) (int, error) {
	original := len(content)
	if writer.remaining > 0 {
		if len(content) > writer.remaining {
			content = content[:writer.remaining]
		}
		writer.remaining -= len(content)
		_, _ = writer.destination.Write(content)
	}
	return original, nil
}
