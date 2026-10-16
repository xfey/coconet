package inference

import (
	"encoding/json"
	"errors"
)

// Codex App Server stdout is JSON-RPC only. Never copy unexpected provider
// output into errors: it may contain credentials or private conversation text.
func decodeRPCFrame(line []byte) (frame rpcFrame, err error) {
	if json.Unmarshal(line, &frame) != nil {
		return frame, errors.New("app-server emitted invalid protocol JSON")
	}
	response := len(frame.ID) > 0 && string(frame.ID) != "null" && (len(frame.Result) > 0 != (len(frame.Error) > 0))
	if frame.Method == "" && !response {
		return frame, errors.New("app-server emitted an invalid protocol frame")
	}
	return frame, nil
}
