package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/xfey/coconet/packages/workgraph"
)

// Completed operations keep only request identity and the resumable child.
// Receipts are separate from active input roots and are not scanned by GC.
type localForkReceipt struct {
	Schema        int                   `json:"schema"`
	RequestSHA256 string                `json:"request_sha256"`
	Result        localNativeForkResult `json:"result"`
}

func forkReceiptDirectory(directory string) (string, error) {
	path := filepath.Join(directory, "completed")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid completed Fork directory")
	}
	return path, nil
}

func readForkReceipt(directory, key string, request localNativeForkRequest) (localNativeForkResult, bool, error) {
	data, err := readLocalPrivateFile(filepath.Join(directory, key+".json"), 64<<10, false)
	if errors.Is(err, os.ErrNotExist) {
		return localNativeForkResult{}, false, nil
	}
	if err != nil {
		return localNativeForkResult{}, false, err
	}
	var receipt localForkReceipt
	if err := decodeStrict(data, &receipt); err != nil || receipt.Schema != 1 || receipt.Result.State != "created" || receipt.Result.NativeID == "" {
		return localNativeForkResult{}, false, errors.New("invalid completed Fork receipt; retain the existing files and inspect diagnostics before retrying")
	}
	if receipt.RequestSHA256 != workgraph.Digest(request) {
		return localNativeForkResult{}, false, errors.New("Fork request_id already selects different source, Agent or workspace; retry with the original arguments")
	}
	return receipt.Result, true, nil
}

func completeForkReceipt(directory, key string, request localNativeForkRequest, result localNativeForkResult) error {
	completed, err := forkReceiptDirectory(directory)
	if err != nil {
		return err
	}
	if err := atomicWriteJSON(filepath.Join(completed, key+".json"), localForkReceipt{Schema: 1, RequestSHA256: workgraph.Digest(request), Result: result}, 0600); err != nil {
		return err
	}
	// The durable receipt is sufficient for an exact retry. These files are
	// adapter staging/operation metadata; the native child lives in its profile.
	// A cleanup failure cannot turn a confirmed creation into another creation.
	_ = os.RemoveAll(filepath.Join(directory, key))
	return nil
}
