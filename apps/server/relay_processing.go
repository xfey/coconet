package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
)

func (h *serverHandler) serveRelayProcessing(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string, parts []string, storage *relay.Store, failure func(error)) bool {
	if len(parts) == 0 || parts[0] != "processing" {
		return false
	}
	if r.Method == http.MethodGet {
		if len(parts) == 1 {
			after := int64(0)
			if value := r.URL.Query().Get("after"); value != "" {
				var err error
				after, err = strconv.ParseInt(value, 10, 64)
				if err != nil {
					failure(relay.ErrInvalid)
					return true
				}
			}
			page, err := storage.ProcessingTasks(project, after)
			if err != nil {
				failure(err)
			} else {
				h.writeJSON(w, 200, page)
			}
		} else if len(parts) == 2 {
			task, err := storage.ProcessingTask(project, parts[1])
			if err != nil {
				failure(err)
			} else {
				h.writeJSON(w, 200, task)
			}
		} else {
			failure(relay.ErrInvalid)
		}
		return true
	}
	if r.Method != http.MethodPost || len(parts) > 2 {
		h.methodNotAllowed(w, requestID, "GET, POST")
		return true
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	var result contracts.ProcessingTask
	var err error
	if len(parts) == 1 {
		var input contracts.ProcessingRegister
		if err = decoder.Decode(&input); err == nil && decoder.Decode(&struct{}{}) == io.EOF {
			result, err = storage.RegisterProcessing(project, actor.AccountID, input)
		} else {
			err = relay.ErrInvalid
		}
	} else {
		var input contracts.ProcessingAction
		if err = decoder.Decode(&input); err == nil && decoder.Decode(&struct{}{}) == io.EOF {
			result, err = storage.ActProcessing(project, actor.AccountID, parts[1], input)
		} else {
			err = relay.ErrInvalid
		}
	}
	if err != nil {
		failure(err)
	} else {
		h.writeJSON(w, 200, result)
	}
	return true
}
