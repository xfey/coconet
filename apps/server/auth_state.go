package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/xfey/coconet/packages/contracts"
)

const authorizationTTL = 10 * time.Minute
const deviceCredentialTTL = 90 * 24 * time.Hour

// Authorization requests are short-lived and contain no provider access tokens.
// Both metadata backends consume approval and issue the device token atomically.
type authorizationRecord struct {
	ReturnProjectID    string                         `json:"return_project_id,omitempty"`
	Kind               string                         `json:"kind"`
	ExpiresAt          string                         `json:"expires_at"`
	AuthorizationID    string                         `json:"authorization_id,omitempty"`
	Challenge          string                         `json:"challenge,omitempty"`
	DeviceName         string                         `json:"device_name,omitempty"`
	CallbackURL        string                         `json:"callback_url,omitempty"`
	AccountID          string                         `json:"account_id,omitempty"`
	Denied             bool                           `json:"denied,omitempty"`
	BrowserHash        string                         `json:"browser_hash,omitempty"`
	Verifier           string                         `json:"verifier,omitempty"`
	Nonce              string                         `json:"nonce,omitempty"`
	Provider           string                         `json:"provider,omitempty"`
	ConnectProject     bool                           `json:"connect_project,omitempty"`
	ProjectName        string                         `json:"project_name,omitempty"`
	ProjectID          string                         `json:"project_id,omitempty"`
	ExpectedAccountID  string                         `json:"expected_account_id,omitempty"`
	SuggestedProjectID string                         `json:"suggested_project_id,omitempty"`
	Directory          *contracts.ConnectionDirectory `json:"directory,omitempty"`
	DirectoryRequestID string                         `json:"directory_request_id,omitempty"`
	DirectoryError     string                         `json:"directory_error,omitempty"`
}

func validDisplayName(value string) bool {
	return value != "" && len(value) <= 120 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func pruneAuthorizations(state *databaseState, now time.Time) {
	for key, request := range state.Authorizations.All() {
		if !authTimeActive(request.ExpiresAt, now) {
			state.Authorizations.Delete(key)
		}
	}
}

func authTimeActive(value string, now time.Time) bool {
	expires, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && expires.After(now)
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func validLoopbackCallback(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User == nil && u.Path == "/callback" && u.RawQuery == "" && u.Fragment == ""
}

func (a *authority) startDeviceAuthorization(input contracts.DeviceAuthorizationRequest, origin string) (contracts.DeviceAuthorization, error) {
	name := strings.TrimSpace(input.DeviceName)
	challenge, err := base64.RawURLEncoding.DecodeString(input.CodeChallenge)
	if !validDisplayName(name) || err != nil || len(challenge) != 32 || len(input.CodeChallenge) != 43 || !validLoopbackCallback(input.CallbackURL) {
		return contracts.DeviceAuthorization{}, invalid("invalid_device_authorization", "a device name, S256 challenge and optional loopback callback are required")
	}
	invalidProject := len(input.ExpectedAccountID) > 128 || len(input.SuggestedProjectID) > 128 ||
		input.ProjectName != "" && (!input.ConnectProject || !validDisplayName(input.ProjectName)) ||
		input.SuggestedProjectID != "" && !input.ConnectProject
	invalidDirectory := input.Directory != nil && (!input.ConnectProject || !validConnectionDirectory(*input.Directory) || input.Directory.Revision != 1)
	if invalidProject || invalidDirectory {
		return contracts.DeviceAuthorization{}, invalid("invalid_device_authorization", "invalid project setup request")
	}
	secret, err := randomSecret(32)
	if err != nil {
		return contracts.DeviceAuthorization{}, err
	}
	code, err := randomSecret(32)
	if err != nil {
		return contracts.DeviceAuthorization{}, err
	}
	err = a.store.update(func(state *databaseState) error {
		pruneAuthorizations(state, a.now())
		if state.Authorizations.Len() >= 1000 {
			return rateLimitError()
		}
		for _, old := range state.Authorizations.All() {
			if old.AuthorizationID == code {
				return errors.New("authorization code collision; retry login")
			}
		}
		state.Authorizations.Set(hashToken(secret), authorizationRecord{
			Kind: "device", AuthorizationID: code, Challenge: input.CodeChallenge, DeviceName: name,
			CallbackURL: input.CallbackURL, ExpiresAt: a.now().Add(authorizationTTL).UTC().Format(time.RFC3339Nano),
			ConnectProject: input.ConnectProject, ProjectName: input.ProjectName, ExpectedAccountID: input.ExpectedAccountID,
			SuggestedProjectID: input.SuggestedProjectID, Directory: input.Directory,
		})
		return nil
	})
	return contracts.DeviceAuthorization{Status: contracts.StatusPending, DeviceCode: secret, AuthorizationID: code, VerificationURI: origin + "/#authorize=" + code, ExpiresIn: int(authorizationTTL.Seconds()), Interval: 2}, err
}

func (a *authority) decideDeviceAuthorization(actor contracts.Account, input contracts.DeviceAuthorizationDecision) (contracts.DeviceAuthorizationPreview, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(input.AuthorizationID); err != nil || len(input.AuthorizationID) != 43 || len(decoded) != 32 {
		return contracts.DeviceAuthorizationPreview{}, invalid("invalid_device_authorization", "invalid authorization identifier")
	}
	var result contracts.DeviceAuthorizationPreview
	operation := func(state *databaseState) error {
		for key, request := range state.Authorizations.All() {
			if request.Kind != "device" || !authTimeActive(request.ExpiresAt, a.now()) || request.AuthorizationID != input.AuthorizationID {
				continue
			}
			if request.AccountID != "" || request.Denied {
				return conflict("authorization_completed", "this authorization has already been decided")
			}
			if request.ExpectedAccountID != "" && request.ExpectedAccountID != actor.AccountID {
				return forbidden("authorization_account_mismatch", "sign in with the account already connected on this device")
			}
			result.DeviceName = request.DeviceName
			result.ConnectProject, result.ProjectName = request.ConnectProject, request.ProjectName
			result.SuggestedProjectID, result.Directory = request.SuggestedProjectID, request.Directory
			result.DirectoryPending, result.DirectoryError = request.DirectoryRequestID != "", request.DirectoryError
			if input.ChooseDirectory {
				if input.Approve != nil || request.Directory == nil || !request.Directory.CanBrowse {
					return invalid("directory_chooser_unavailable", "choose the folder in the CLI on this device")
				}
				if input.DirectoryRevision != request.Directory.Revision {
					return conflict("directory_changed", "review the current directory before continuing")
				}
				if request.DirectoryRequestID == "" {
					id, err := randomID("folder")
					if err != nil {
						return err
					}
					request.DirectoryRequestID, request.DirectoryError = id, ""
					state.Authorizations.Set(key, request)
				}
				result.DirectoryPending, result.DirectoryError = true, ""
			}
			if input.Approve != nil {
				if *input.Approve {
					if request.Directory != nil && (request.DirectoryRequestID != "" || input.DirectoryRevision != request.Directory.Revision) {
						return conflict("directory_changed", "review the current directory before approving")
					}
					if request.ConnectProject {
						if _, err := a.joinProjectInState(state, actor, input.ProjectID, key); err != nil {
							return err
						}
						request.ProjectID = input.ProjectID
					} else if input.ProjectID != "" {
						return invalid("invalid_device_authorization", "login does not select a project")
					}
					request.AccountID = actor.AccountID
					result.CallbackURL = request.CallbackURL
				} else {
					request.Denied = true
				}
				state.Authorizations.Set(key, request)
			}
			return nil
		}
		return notFound("authorization_expired", "authorization was not found or has expired; start login again")
	}
	var err error
	if input.Approve == nil && !input.ChooseDirectory {
		err = a.store.view(func(state databaseState) error { return operation(&state) })
	} else {
		err = a.store.update(operation)
	}
	return result, err
}

func (a *authority) pollDeviceAuthorization(input contracts.DeviceAuthorizationPoll) (contracts.DeviceAuthorization, error) {
	result := contracts.DeviceAuthorization{Status: contracts.StatusPending}
	if len(input.DeviceCode) > 100 || len(input.CodeVerifier) < 43 || len(input.CodeVerifier) > 128 {
		return result, invalid("invalid_device_authorization", "invalid device authorization")
	}
	ready := false
	err := a.store.view(func(state databaseState) error {
		request, ok := state.Authorizations.Lookup(hashToken(input.DeviceCode))
		if !ok || request.Kind != "device" || !authTimeActive(request.ExpiresAt, a.now()) {
			return notFound("authorization_expired", "authorization expired; start login again")
		}
		if request.Challenge != pkceChallenge(input.CodeVerifier) {
			return forbidden("authorization_verifier_invalid", "authorization verifier does not match")
		}
		if request.Denied {
			return forbidden("authorization_denied", "device authorization was declined")
		}
		ready = request.AccountID != ""
		if !ready {
			result.DirectoryRequestID = request.DirectoryRequestID
		}
		return nil
	})
	if err != nil || !ready {
		return result, err
	}
	err = a.store.update(func(state *databaseState) error {
		key := hashToken(input.DeviceCode)
		request, ok := state.Authorizations.Lookup(key)
		if !ok || request.Kind != "device" || !authTimeActive(request.ExpiresAt, a.now()) {
			return notFound("authorization_expired", "authorization expired; start login again")
		}
		if request.Challenge != pkceChallenge(input.CodeVerifier) {
			return forbidden("authorization_verifier_invalid", "authorization verifier does not match")
		}
		if request.Denied {
			return forbidden("authorization_denied", "device authorization was declined")
		}
		if request.AccountID == "" {
			return nil
		}
		account, ok := state.Accounts.Lookup(request.AccountID)
		if !ok {
			return errors.New("authorization account is missing")
		}
		if request.ConnectProject {
			if _, _, err := requireActiveMember(state, account.AccountID, request.ProjectID); err != nil {
				return err
			}
		}
		secret, err := randomSecret(32)
		if err != nil {
			return err
		}
		id, err := randomID("device")
		if err != nil {
			return err
		}
		count := 0
		for hash, record := range state.Tokens.All() {
			if record.DeviceID == "" {
				continue
			}
			if !authTimeActive(record.ExpiresAt, a.now()) {
				state.Tokens.Delete(hash)
				continue
			}
			if record.AccountID == account.AccountID {
				count++
			}
		}
		if count >= 30 {
			return conflict("device_limit_reached", "remove an unused device in account settings before signing in")
		}
		state.Tokens.Set(hashToken(secret), tokenRecord{AccountID: account.AccountID, DeviceID: id, DeviceName: request.DeviceName, CreatedAt: a.timestamp(), ExpiresAt: a.now().Add(deviceCredentialTTL).UTC().Format(time.RFC3339Nano)})
		state.Authorizations.Delete(key)
		result.Status, result.AccessToken, result.Account = contracts.StatusOK, secret, &account
		result.ProjectID, result.Directory = request.ProjectID, request.Directory
		return nil
	})
	return result, err
}

func (a *authority) externalAccount(provider, subject, name, email string) (contracts.Account, error) {
	var result contracts.Account
	if subject == "" || len(subject) > 512 {
		return result, invalid("invalid_login_identity", "login provider did not return a stable identity")
	}
	name = strings.TrimSpace(name)
	if !validDisplayName(name) {
		name = "Coconet user"
	}
	key := hashToken(provider + "\x00" + subject)
	err := a.store.update(func(state *databaseState) error {
		if id := state.ExternalIdentities.Get(key); id != "" {
			result = state.Accounts.Get(id)
			if result.AccountID == "" {
				return errors.New("external identity account is missing")
			}
			return nil
		}
		id, err := randomID("acct")
		if err != nil {
			return err
		}
		result = contracts.Account{SchemaVersion: 1, AccountID: id, DisplayName: name, Email: email, CreatedAt: a.timestamp()}
		state.Accounts.Set(id, result)
		state.ExternalIdentities.Set(key, id)
		return nil
	})
	return result, err
}

func (a *authority) accountDevices(actor contracts.Account) (contracts.AccountDevices, error) {
	result := contracts.AccountDevices{Devices: []contracts.AccountDevice{}}
	err := a.store.view(func(state databaseState) error {
		for _, token := range state.Tokens.By("account_id", actor.AccountID) {
			if token.AccountID == actor.AccountID && token.DeviceID != "" && authTimeActive(token.ExpiresAt, a.now()) {
				result.Devices = append(result.Devices, contracts.AccountDevice{ID: token.DeviceID, Name: token.DeviceName, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
			}
		}
		return nil
	})
	sort.Slice(result.Devices, func(i, j int) bool {
		if result.Devices[i].CreatedAt == result.Devices[j].CreatedAt {
			return result.Devices[i].ID < result.Devices[j].ID
		}
		return result.Devices[i].CreatedAt > result.Devices[j].CreatedAt
	})
	return result, err
}

func (a *authority) revokeDevice(actor contracts.Account, id string) error {
	return a.store.update(func(state *databaseState) error {
		for key, record := range state.Tokens.By("account_id", actor.AccountID) {
			if record.AccountID == actor.AccountID && record.DeviceID == id && id != "" {
				state.Tokens.Delete(key)
				return nil
			}
		}
		return notFound("device_not_found", "device was not found")
	})
}

func (a *authority) updateAccountProfile(actor contracts.Account, input contracts.AccountProfileUpdate) (contracts.Account, error) {
	name := strings.TrimSpace(input.DisplayName)
	if !validDisplayName(name) {
		return actor, invalid("invalid_display_name", "name must contain 1 to 120 visible characters")
	}
	err := a.store.update(func(state *databaseState) error {
		actor = state.Accounts.Get(actor.AccountID)
		if actor.DisplayName == name {
			return nil
		}
		actor.DisplayName = name
		state.Accounts.Set(actor.AccountID, actor)
		for key, membership := range state.Memberships.By("account_id", actor.AccountID) {
			if membership.Account.AccountID == actor.AccountID {
				membership.Account = actor
				state.Memberships.Set(key, membership)
				markDashboardChanged(state, membership.ProjectID)
			}
		}
		return nil
	})
	return actor, err
}
