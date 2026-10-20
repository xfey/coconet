package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/projectlink"
)

type authorityError struct {
	HTTPStatus        int
	Status            contracts.Status
	Code              string
	Message           string
	Retryable         bool
	RetryAfterSeconds int
}

func (e *authorityError) Error() string { return e.Message }

func invalid(code, message string) error {
	return &authorityError{HTTPStatus: 400, Status: contracts.StatusInvalidRequest, Code: code, Message: message}
}

func forbidden(code, message string) error {
	return &authorityError{HTTPStatus: 403, Status: contracts.StatusForbidden, Code: code, Message: message}
}

func notFound(code, message string) error {
	return &authorityError{HTTPStatus: 404, Status: contracts.StatusNotFound, Code: code, Message: message}
}

type authority struct {
	store     *store
	artifacts artifactStore
	now       func() time.Time
	limits    deploymentLimits
}

func newAuthority(store *store, artifacts ...artifactStore) *authority {
	result := &authority{store: store, now: time.Now, limits: defaultDeploymentLimits()}
	if len(artifacts) > 0 {
		result.artifacts = artifacts[0]
	}
	return result
}

func (a *authority) bootstrapAccount(request contracts.AccountBootstrapRequest) (contracts.AccessToken, error) {
	displayName := strings.TrimSpace(request.DisplayName)
	email := strings.TrimSpace(request.Email)
	if displayName == "" || len(displayName) > 120 || strings.IndexFunc(displayName, unicode.IsControl) >= 0 {
		return contracts.AccessToken{}, invalid("invalid_display_name", "display_name must contain 1 to 120 characters")
	}
	parsedEmail, err := mail.ParseAddress(email)
	if email != "" && (err != nil || parsedEmail.Address != email || len(email) > 254) {
		return contracts.AccessToken{}, invalid("invalid_email", "email must be a valid address")
	}
	accountID, err := randomID("acct")
	if err != nil {
		return contracts.AccessToken{}, err
	}
	token, err := randomSecret(32)
	if err != nil {
		return contracts.AccessToken{}, err
	}
	account := contracts.Account{
		SchemaVersion: contracts.SchemaVersion,
		AccountID:     accountID,
		DisplayName:   displayName,
		Email:         email,
		CreatedAt:     a.timestamp(),
	}
	if err := a.store.update(func(state *databaseState) error {
		state.Accounts.Set(accountID, account)
		state.Tokens.Set(hashToken(token), tokenRecord{AccountID: accountID})
		return nil
	}); err != nil {
		return contracts.AccessToken{}, err
	}
	return contracts.AccessToken{
		SchemaVersion: contracts.SchemaVersion,
		Status:        contracts.StatusOK,
		AccessToken:   token,
		TokenType:     "Bearer",
		Account:       &account,
	}, nil
}

func (a *authority) authenticate(token string) (contracts.Account, error) {
	if token == "" {
		return contracts.Account{}, &authorityError{HTTPStatus: 401, Status: contracts.StatusForbidden, Code: "authentication_required", Message: "authentication is required"}
	}
	var account contracts.Account
	err := a.store.view(func(state databaseState) error {
		record, ok := state.Tokens.Lookup(hashToken(token))
		if !ok || record.DeviceID != "" && !authTimeActive(record.ExpiresAt, a.now()) {
			return &authorityError{HTTPStatus: 401, Status: contracts.StatusForbidden, Code: "invalid_token", Message: "access token is invalid"}
		}
		var found bool
		account, found = state.Accounts.Lookup(record.AccountID)
		if !found {
			return errors.New("token references a missing account")
		}
		return nil
	})
	return account, err
}

func validProjectName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 120
}

func (a *authority) createProject(actor contracts.Account, request contracts.CreateProjectRequest) (contracts.ProjectAccess, error) {
	name := strings.TrimSpace(request.Name)
	if !validProjectName(name) {
		return contracts.ProjectAccess{}, invalid("invalid_project_name", "project name must contain 1 to 120 characters")
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return contracts.ProjectAccess{}, err
	}
	var project contracts.Project
	var membership contracts.Membership
	err := a.store.update(func(state *databaseState) error {
		opKey := operationKey(actor.AccountID, "project.create", request.RequestID)
		if operation, ok := state.Operations.Lookup(opKey); ok {
			project = state.Projects.Get(operation.EntityID)
			membership = state.Memberships.Get(membershipKey(project.ProjectID, actor.AccountID))
			return nil
		}
		projectCount := 0
		for _, existing := range state.Projects.All() {
			if existing.CreatedBy == actor.AccountID && existing.Status == contracts.ProjectActive {
				projectCount++
			}
		}
		if projectCount >= a.limits.ProjectsPerAccount {
			return conflict("project_limit_reached", fmt.Sprintf("this server allows %d active projects per creator; archive unused projects or ask the administrator to adjust COCONET_MAX_PROJECTS_PER_ACCOUNT", a.limits.ProjectsPerAccount))
		}
		var projectID string
		for attempt := 0; attempt < 8; attempt++ {
			var raw [projectlink.ProjectUIDSize]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return err
			}
			projectID = projectlink.NewProjectID(raw)
			if collision := state.Projects.Has(projectID); !collision {
				break
			}
			projectID = ""
		}
		if projectID == "" {
			return errors.New("could not allocate a unique Project ID")
		}
		now := a.timestamp()
		project = contracts.Project{
			SchemaVersion: contracts.SchemaVersion,
			ProjectID:     projectID,
			Name:          name,
			CreatedBy:     actor.AccountID,
			OwnerID:       actor.AccountID,
			Status:        contracts.ProjectActive,
			CreatedAt:     now,
		}
		membership = contracts.Membership{
			SchemaVersion: contracts.SchemaVersion,
			ProjectID:     projectID,
			Account:       actor,
			Role:          contracts.RoleMember,
			Status:        contracts.MembershipActive,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		state.Projects.Set(projectID, project)
		state.Memberships.Set(membershipKey(projectID, actor.AccountID), membership)
		state.Operations.Set(opKey, operationRecord{EntityID: projectID, Action: "project.create"})
		return a.recordAudit(state, request.RequestID, projectID, actor.AccountID, "project.create", projectID)
	})
	if err != nil {
		return contracts.ProjectAccess{}, err
	}
	return accessFor(project, membership, nil), nil
}

func (a *authority) projectAccess(actor contracts.Account, projectID string) (contracts.ProjectAccess, error) {
	var result contracts.ProjectAccess
	err := a.store.view(func(state databaseState) error {
		project, ok := state.Projects.Lookup(projectID)
		if !ok {
			return notFound("project_not_found", "project was not found")
		}
		membership, hasMembership := state.Memberships.Lookup(membershipKey(projectID, actor.AccountID))
		if hasMembership && (membership.Status == contracts.MembershipActive || membership.Status == contracts.MembershipArchived) {
			result = accessFor(project, membership, nil)
			return nil
		}
		result = contracts.ProjectAccess{SchemaVersion: contracts.SchemaVersion, Status: contracts.StatusForbidden, Capabilities: emptyCapabilities()}
		return nil
	})
	return result, err
}

// joinProject requires an explicit join action. Reads never create membership.
func (a *authority) joinProject(actor contracts.Account, projectID, requestID string) (contracts.Membership, error) {
	if err := validateRequestID(requestID); err != nil {
		return contracts.Membership{}, err
	}
	var result contracts.Membership
	err := a.store.update(func(state *databaseState) error {
		var err error
		result, err = a.joinProjectInState(state, actor, projectID, requestID)
		return err
	})
	return result, err
}

// Called after all authorization checks, in the same transaction as approval.
func (a *authority) joinProjectInState(state *databaseState, actor contracts.Account, projectID, requestID string) (contracts.Membership, error) {
	if _, ok := projectlink.ProjectUID(projectID); !ok {
		return contracts.Membership{}, invalid("invalid_project_id", "enter a valid Project ID")
	}
	project, ok := state.Projects.Lookup(projectID)
	if !ok {
		return contracts.Membership{}, notFound("project_not_found", "project was not found")
	}
	if project.Status != contracts.ProjectActive {
		return contracts.Membership{}, &authorityError{HTTPStatus: 409, Status: contracts.StatusProjectArchived, Code: "project_archived", Message: "archived projects do not accept connections"}
	}
	memberKey := membershipKey(projectID, actor.AccountID)
	current, hasMembership := state.Memberships.Lookup(memberKey)
	if hasMembership && current.Status == contracts.MembershipActive {
		return current, nil
	}
	activeMembers := 0
	for _, member := range state.Memberships.By("project_id", projectID) {
		if member.Status == contracts.MembershipActive {
			activeMembers++
		}
	}
	if activeMembers >= a.limits.MembersPerProject {
		return contracts.Membership{}, conflict("project_member_limit_reached", fmt.Sprintf("this server allows %d active members per project; ask the administrator to adjust COCONET_MAX_MEMBERS_PER_PROJECT", a.limits.MembersPerProject))
	}
	timestamp := a.timestamp()
	createdAt := timestamp
	if hasMembership && current.CreatedAt != "" {
		createdAt = current.CreatedAt
	}
	result := contracts.Membership{SchemaVersion: contracts.SchemaVersion, ProjectID: projectID, Account: actor, Role: contracts.RoleMember, Status: contracts.MembershipActive, CreatedAt: createdAt, UpdatedAt: timestamp}
	state.Memberships.Set(memberKey, result)
	return result, a.recordAudit(state, requestID, projectID, actor.AccountID, "project.join", actor.AccountID)
}

func (a *authority) requestJoin(actor contracts.Account, projectID string, request contracts.ProjectMutationRequest) (contracts.ProjectAccess, error) {
	return contracts.ProjectAccess{}, deprecatedFlow()
}

func (a *authority) management(actor contracts.Account, projectID string) (contracts.ProjectManagement, error) {
	var result contracts.ProjectManagement
	err := a.store.view(func(state databaseState) error {
		project, ok := state.Projects.Lookup(projectID)
		if !ok {
			return notFound("project_not_found", "project was not found")
		}
		current, ok := state.Memberships.Lookup(membershipKey(projectID, actor.AccountID))
		if !ok || current.Status != contracts.MembershipActive && current.Status != contracts.MembershipArchived {
			return forbidden("membership_required", "active project membership is required")
		}
		result = contracts.ProjectManagement{
			SchemaVersion: contracts.SchemaVersion,
			Project:       project,
			Current:       current,
			Members:       []contracts.Membership{current},
			JoinRequests:  []contracts.JoinRequest{},
		}
		return nil
	})
	return result, err
}

func (a *authority) decideJoin(actor contracts.Account, projectID, joinRequestID string, request contracts.JoinDecisionRequest) (contracts.JoinRequest, error) {
	return contracts.JoinRequest{}, deprecatedFlow()
}

func (a *authority) removeMember(actor contracts.Account, projectID, accountID string, request contracts.ProjectMutationRequest) (contracts.Membership, error) {
	return contracts.Membership{}, deprecatedFlow()
}

func (a *authority) leaveProject(actor contracts.Account, projectID string, request contracts.ProjectMutationRequest) (contracts.Membership, error) {
	if err := validateRequestID(request.RequestID); err != nil {
		return contracts.Membership{}, err
	}
	var member contracts.Membership
	err := a.store.update(func(state *databaseState) error {
		state.dashboardAccessChanged = true
		if ok := state.Projects.Has(projectID); !ok {
			return notFound("project_not_found", "project was not found")
		}
		key := membershipKey(projectID, actor.AccountID)
		var ok bool
		member, ok = state.Memberships.Lookup(key)
		if !ok {
			return forbidden("membership_required", "active project membership is required")
		}
		if member.Status == contracts.MembershipLeft {
			return nil
		}
		if member.Status != contracts.MembershipActive && member.Status != contracts.MembershipArchived {
			return forbidden("membership_required", "active project membership is required")
		}
		member.Status = contracts.MembershipLeft
		member.UpdatedAt = a.timestamp()
		state.Memberships.Set(key, member)
		opKey := operationKey(actor.AccountID, "member.leave", request.RequestID)
		state.Operations.Set(opKey, operationRecord{EntityID: actor.AccountID, Action: "member.leave"})
		return a.recordAudit(state, request.RequestID, projectID, actor.AccountID, "member.leave", actor.AccountID)
	})
	return member, err
}

func (a *authority) dissolveProject(actor contracts.Account, projectID string, request contracts.ProjectMutationRequest) (contracts.Project, error) {
	return contracts.Project{}, deprecatedFlow()
}

func deprecatedFlow() error {
	return conflict("deprecated_flow", "approval and member management are no longer supported; join using the Project ID")
}

func accessFor(project contracts.Project, membership contracts.Membership, join *contracts.JoinRequest) contracts.ProjectAccess {
	status := contracts.StatusOK
	capabilities := map[string]bool{
		"read":   true,
		"write":  project.Status == contracts.ProjectActive,
		"manage": false,
		"resume": project.Status == contracts.ProjectActive,
		"share":  project.Status == contracts.ProjectActive,
	}
	if project.Status == contracts.ProjectArchived {
		status = contracts.StatusProjectArchived
	}
	return contracts.ProjectAccess{
		SchemaVersion: contracts.SchemaVersion,
		Status:        status,
		Project:       &project,
		Membership:    &membership,
		JoinRequest:   join,
		Capabilities:  capabilities,
	}
}

func emptyCapabilities() map[string]bool {
	return map[string]bool{"read": false, "write": false, "manage": false, "resume": false, "share": false}
}

func (a *authority) recordAudit(state *databaseState, requestID, projectID, actorID, action, targetID string) error {
	auditID, err := randomID("audit")
	if err != nil {
		return err
	}
	state.AuditEvents.Set(auditID, contracts.AuditEvent{
		SchemaVersion: contracts.SchemaVersion,
		AuditEventID:  auditID,
		RequestID:     requestID,
		ProjectID:     projectID,
		ActorID:       actorID,
		Action:        action,
		TargetID:      targetID,
		Outcome:       "succeeded",
		OccurredAt:    a.timestamp(),
	})
	return nil
}

func (a *authority) timestamp() string { return a.now().UTC().Format(time.RFC3339Nano) }

func membershipKey(projectID, accountID string) string { return projectID + ":" + accountID }

func operationKey(actorID, action, requestID string) string {
	return actorID + ":" + action + ":" + requestID
}

func validateRequestID(value string) error {
	if len(strings.TrimSpace(value)) < 8 || len(value) > 200 {
		return invalid("invalid_request_id", "request_id must contain 8 to 200 characters")
	}
	return nil
}

func randomID(prefix string) (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomSecret(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
