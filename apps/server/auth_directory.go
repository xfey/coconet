package main

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
)

// These are paths on the requesting device. Never inspect the server filesystem.
func validDevicePath(value string) bool {
	return len(value) > 0 && len(value) <= 4096 && utf8.ValidString(value) && strings.HasPrefix(value, "/") && path.Clean(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validConnectionDirectory(directory contracts.ConnectionDirectory) bool {
	return validDevicePath(directory.CWD) && validDevicePath(directory.Path) && directory.Revision > 0 && !(directory.Remote && directory.CanBrowse) && (directory.CWD == directory.Path || strings.HasPrefix(directory.CWD, strings.TrimSuffix(directory.Path, "/")+"/"))
}

func (a *authority) updateDeviceDirectory(input contracts.DeviceDirectoryUpdate) error {
	if len(input.DeviceCode) > 100 || len(input.CodeVerifier) < 43 || len(input.CodeVerifier) > 128 || input.RequestID == "" || len(input.RequestID) > 128 {
		return invalid("invalid_directory", "invalid directory update")
	}
	if input.Error != "" && input.Error != "cancelled" && input.Error != "unavailable" && input.Error != "invalid" && input.Error != "already_connected" {
		return invalid("invalid_directory", "invalid directory selection result")
	}
	return a.store.update(func(state *databaseState) error {
		key := hashToken(input.DeviceCode)
		request, ok := state.Authorizations.Lookup(key)
		if !ok || request.Kind != "device" || !authTimeActive(request.ExpiresAt, a.now()) {
			return notFound("authorization_expired", "authorization expired; run coconet connect again")
		}
		if request.Challenge != pkceChallenge(input.CodeVerifier) {
			return forbidden("authorization_verifier_invalid", "authorization verifier does not match")
		}
		if request.AccountID != "" || request.Denied || request.Directory == nil || request.DirectoryRequestID != input.RequestID {
			return conflict("directory_request_completed", "this folder request is no longer active")
		}
		directory := *request.Directory
		if input.Error == "" {
			directory.CWD, directory.Path = input.CWD, input.Path
			if !validConnectionDirectory(directory) {
				return invalid("invalid_directory", "a canonical directory on the CLI device is required")
			}
			if name := path.Base(directory.Path); validDisplayName(name) {
				request.ProjectName = name
			}
		} else if input.CWD != "" || input.Path != "" {
			return invalid("invalid_directory", "a cancelled or failed selection cannot change the directory")
		}
		directory.Revision++
		request.Directory = &directory
		request.DirectoryRequestID, request.DirectoryError = "", input.Error
		state.Authorizations.Set(key, request)
		return nil
	})
}
