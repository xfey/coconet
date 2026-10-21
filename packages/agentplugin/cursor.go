package agentplugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Cursor currently reads extensions/hooks from ~/.cursor, even when its chat
// and settings profile is relocated through CURSOR_CONFIG_DIR.
func (m Manager) cursorRoot() (string, error) {
	if m.CursorRoot != "" {
		return filepath.Abs(m.CursorRoot)
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".cursor"), err
}
func (m Manager) installCursor(result InstallResult, marketplace string) InstallResult {
	fail := func(err error) InstallResult { result.Status = StatusFailed; result.Error = err.Error(); return result }
	root, err := m.cursorRoot()
	if err != nil {
		return fail(err)
	}
	parent := filepath.Join(root, "plugins", "local")
	target := filepath.Join(parent, "coconet")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return fail(err)
	}
	exists, err := cursorOwnedPlugin(target)
	if err != nil {
		return fail(err)
	}
	stage, err := os.MkdirTemp(parent, ".coconet-install-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(stage)
	if err = copyCursorPlugin(filepath.Join(marketplace, "plugins", "coconet"), stage); err != nil {
		return fail(err)
	}
	if err = os.WriteFile(filepath.Join(stage, ".coconet-managed"), []byte("cursor-plugin/1\n"), 0600); err != nil {
		return fail(err)
	}
	backup := ""
	if exists {
		backup, err = os.MkdirTemp(parent, ".coconet-previous-")
		if err != nil {
			return fail(err)
		}
		os.Remove(backup)
		if err = os.Rename(target, backup); err != nil {
			return fail(err)
		}
	}
	restore := func() {
		os.RemoveAll(target)
		if backup != "" {
			_ = os.Rename(backup, target)
		}
	}
	if err = os.Rename(stage, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return fail(err)
	}
	if _, _, err = editCursorHooks(root, true); err != nil {
		restore()
		return fail(err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	result.Status = StatusConfigured
	return result
}
func (m Manager) uninstallCursor(result UninstallResult) UninstallResult {
	fail := func(err error) UninstallResult {
		result.Status = StatusFailed
		result.Error = err.Error()
		return result
	}
	root, err := m.cursorRoot()
	if err != nil {
		return fail(err)
	}
	target := filepath.Join(root, "plugins", "local", "coconet")
	exists, err := cursorOwnedPlugin(target)
	if err != nil {
		return fail(err)
	}
	backup, count, err := editCursorHooks(root, false)
	if err != nil {
		return fail(err)
	}
	result.ConfigBackup = backup
	result.ConfigSectionsRemoved = count
	if exists {
		if err = os.RemoveAll(target); err != nil {
			return fail(err)
		}
		result.CacheRemoved = true
	}
	result.Status = StatusRemoved
	return result
}
func cursorOwnedPlugin(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("Cursor Coconet plugin path is not a managed directory; move the existing entry before retrying")
	}
	b, err := os.ReadFile(filepath.Join(path, ".coconet-managed"))
	if err != nil || string(b) != "cursor-plugin/1\n" {
		return false, errors.New("an unmanaged Cursor plugin named coconet already exists; remove it in Cursor before retrying")
	}
	return true, nil
}
func copyCursorPlugin(source, target string) error {
	manifest, err := os.ReadFile(filepath.Join(source, ".cursor-plugin", "plugin.json"))
	if err != nil {
		return err
	}
	var meta struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(manifest, &meta) != nil || meta.Name != "coconet" {
		return errors.New("invalid bundled Cursor plugin")
	}
	var total int64
	count := 0
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 1024 {
			return errors.New("Cursor plugin bundle exceeds entry limit")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		out := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("Cursor plugin bundle must contain only regular files")
		}
		total += info.Size()
		if total > 16<<20 {
			return errors.New("Cursor plugin bundle exceeds size limit")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm()&0777)
	})
}
func cursorHookCommand(root string, context bool) string {
	script := filepath.Join(root, "plugins", "local", "coconet", "scripts", "hook.sh")
	cmd := "sh '" + strings.ReplaceAll(script, "'", "'\\''") + "'"
	if context {
		cmd += " --context"
	}
	return cmd
}
func editCursorHooks(root string, install bool) (string, int, error) {
	path := filepath.Join(root, "hooks.json")
	target := path
	mode := os.FileMode(0600)
	old := []byte(nil)
	if info, err := os.Lstat(path); err == nil {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", 0, fmt.Errorf("resolve Cursor hooks link: %w; restore its target before retrying", err)
		}
		info, err = os.Stat(target)
		if err != nil {
			return "", 0, err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return "", 0, errors.New("Cursor hooks must be a regular JSON file within 1 MiB")
		}
		mode = info.Mode().Perm()
		old, err = os.ReadFile(target)
		if err != nil {
			return "", 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	} else if !install {
		return "", 0, nil
	}
	obj := map[string]json.RawMessage{}
	if len(old) > 0 {
		if json.Unmarshal(old, &obj) != nil || obj == nil {
			return "", 0, errors.New("Cursor hooks JSON is invalid; correct it before retrying")
		}
	}
	var version int
	if v, ok := obj["version"]; ok && (json.Unmarshal(v, &version) != nil || version != 1) {
		return "", 0, errors.New("unsupported Cursor hooks version")
	}
	hooks := map[string][]json.RawMessage{}
	if h, ok := obj["hooks"]; ok {
		if json.Unmarshal(h, &hooks) != nil || hooks == nil {
			return "", 0, errors.New("Cursor hooks map is invalid")
		}
	}
	count := 0
	for event, entries := range hooks {
		keep := []json.RawMessage{}
		for _, entry := range entries {
			var v struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(entry, &v) != nil {
				return "", 0, errors.New("Cursor hook entry is invalid")
			}
			if v.Command == cursorHookCommand(root, true) || v.Command == cursorHookCommand(root, false) {
				count++
			} else {
				keep = append(keep, entry)
			}
		}
		if len(keep) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keep
		}
	}
	if install {
		for _, event := range []string{"sessionStart", "beforeSubmitPrompt", "stop", "sessionEnd"} {
			entry, _ := json.Marshal(map[string]any{"command": cursorHookCommand(root, event == "sessionStart" || event == "beforeSubmitPrompt"), "timeout": 10})
			hooks[event] = append(hooks[event], entry)
		}
	} else if count == 0 {
		return "", 0, nil
	}
	obj["version"] = json.RawMessage("1")
	obj["hooks"], _ = json.Marshal(hooks)
	updated, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return "", 0, err
	}
	updated = append(updated, '\n')
	if bytes.Equal(old, updated) {
		return "", 0, nil
	}
	backup := ""
	if len(old) > 0 {
		backup, err = createConfigBackup(target, old, mode)
		if err != nil {
			return "", 0, err
		}
		current, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(current, old) {
			return backup, 0, errors.New("Cursor hooks changed during installation; retry after closing configuration editors")
		}
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return backup, 0, err
	}
	return backup, count, atomicWrite(target, updated, mode)
}
