package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/projectlink"
	"golang.org/x/oauth2"
)

type loginProvider struct {
	Kind     string
	Label    string
	Issuer   string
	OAuth    oauth2.Config
	Verifier *oidc.IDTokenVerifier
	UserURL  string
	HTTP     *http.Client
}

func loginProviderFromEnvironment(ctx context.Context, origin string) (*loginProvider, error) {
	kind := strings.TrimSpace(os.Getenv("COCONET_AUTH_PROVIDER"))
	if kind == "" {
		kind = "key"
	}
	provider := &loginProvider{Kind: kind, Label: "Account Key", HTTP: &http.Client{Timeout: 15 * time.Second}}
	if kind == "key" {
		return provider, nil
	}
	if kind != "github" && kind != "oidc" {
		return nil, errors.New("COCONET_AUTH_PROVIDER must be github, oidc or key")
	}
	if !strings.HasPrefix(origin, "https://") {
		return nil, errors.New("GitHub and OIDC login require HTTPS; use account-key login for trusted-network HTTP deployments")
	}
	provider.OAuth = oauth2.Config{ClientID: os.Getenv("COCONET_AUTH_CLIENT_ID"), ClientSecret: os.Getenv("COCONET_AUTH_CLIENT_SECRET"), RedirectURL: origin + "/v1/auth/callback"}
	if provider.OAuth.ClientID == "" || provider.OAuth.ClientSecret == "" {
		return nil, errors.New("login requires COCONET_AUTH_CLIENT_ID and COCONET_AUTH_CLIENT_SECRET")
	}
	if kind == "github" {
		provider.Label, provider.Issuer, provider.UserURL = "GitHub", "https://github.com", "https://api.github.com/user"
		provider.OAuth.Endpoint = oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams}
		return provider, nil
	}
	issuer := strings.TrimSpace(os.Getenv("COCONET_AUTH_ISSUER"))
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("COCONET_AUTH_ISSUER must be an HTTPS OIDC issuer")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, provider.HTTP)
	discovered, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, errors.New("could not discover the configured OIDC provider")
	}
	provider.Label = strings.TrimSpace(os.Getenv("COCONET_AUTH_LABEL"))
	if provider.Label == "" {
		provider.Label = "SSO"
	}
	if !validDisplayName(provider.Label) {
		return nil, errors.New("COCONET_AUTH_LABEL must contain 1 to 120 visible characters")
	}
	provider.Issuer, provider.OAuth.Endpoint = issuer, discovered.Endpoint()
	provider.OAuth.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	provider.Verifier = discovered.Verifier(&oidc.Config{ClientID: provider.OAuth.ClientID})
	return provider, nil
}

func (p *loginProvider) exchange(ctx context.Context, code, verifier, nonce string) (subject, name, email string, err error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.HTTP)
	token, err := p.OAuth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", "", "", errors.New("login provider rejected the authorization code")
	}
	if p.Kind == "oidc" {
		raw, _ := token.Extra("id_token").(string)
		identity, err := p.Verifier.Verify(ctx, raw)
		if err != nil || identity.Nonce != nonce {
			return "", "", "", errors.New("OIDC identity verification failed")
		}
		var claims struct {
			Name              string `json:"name"`
			PreferredUsername string `json:"preferred_username"`
			Email             string `json:"email"`
			EmailVerified     bool   `json:"email_verified"`
		}
		if err := identity.Claims(&claims); err != nil {
			return "", "", "", errors.New("OIDC profile is invalid")
		}
		if claims.Name == "" {
			claims.Name = claims.PreferredUsername
		}
		if !claims.EmailVerified {
			claims.Email = ""
		}
		return identity.Subject, claims.Name, claims.Email, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserURL, nil)
	if err != nil {
		return "", "", "", err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *p.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return "", "", "", errors.New("could not retrieve login profile")
	}
	defer response.Body.Close()
	var profile struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&profile) != nil || profile.ID <= 0 {
		return "", "", "", errors.New("login provider returned an invalid profile")
	}
	if profile.Name == "" {
		profile.Name = profile.Login
	}
	return strconv.FormatInt(profile.ID, 10), profile.Name, "", nil
}

const loginCookieName = "__Host-coconet_login"

func (h *serverHandler) startBrowserLogin(w http.ResponseWriter, r *http.Request, requestID string) {
	var input struct {
		AuthorizationID string `json:"authorization_id"`
		ProjectID       string `json:"project_id"`
	}
	if decodeJSONLimit(r, &input, 1024) != nil || len(input.AuthorizationID) > 43 || len(input.ProjectID) > 128 {
		h.writeError(w, requestID, invalid("invalid_login", "invalid login request"))
		return
	}
	if input.ProjectID != "" {
		if _, ok := projectlink.ProjectUID(input.ProjectID); !ok {
			h.writeError(w, requestID, invalid("invalid_project_id", "invalid Project ID"))
			return
		}
	}
	if h.auth.Kind == "key" {
		h.writeError(w, requestID, invalid("login_provider_unavailable", "this server uses administrator-issued Account Keys"))
		return
	}
	state, err := randomSecret(32)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	browser, err := randomSecret(32)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	nonce, err := randomSecret(32)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	err = h.authority.store.update(func(db *databaseState) error {
		pruneAuthorizations(db, h.authority.now())
		if db.Authorizations.Len() >= 1000 {
			return rateLimitError()
		}
		db.Authorizations.Set(hashToken(state), authorizationRecord{Kind: "browser", BrowserHash: hashToken(browser), Verifier: verifier, Nonce: nonce, Provider: h.auth.Issuer, AuthorizationID: input.AuthorizationID, ReturnProjectID: input.ProjectID, ExpiresAt: h.authority.now().Add(authorizationTTL).UTC().Format(time.RFC3339Nano)})
		return nil
	})
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookieName, Value: browser, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(authorizationTTL.Seconds())})
	loginURL := h.auth.OAuth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce))
	h.writeJSON(w, 200, map[string]string{"url": loginURL})
}

func (h *serverHandler) finishBrowserLogin(w http.ResponseWriter, r *http.Request, requestID string) {
	cookie, err := r.Cookie(loginCookieName)
	state := r.URL.Query().Get("state")
	if err != nil || len(state) > 100 || len(cookie.Value) > 100 {
		http.Redirect(w, r, "/#auth_error=login_failed", http.StatusSeeOther)
		return
	}
	var pending authorizationRecord
	err = h.authority.store.update(func(db *databaseState) error {
		pending = db.Authorizations.Get(hashToken(state))
		if pending.Kind != "browser" || pending.Provider != h.auth.Issuer || pending.BrowserHash != hashToken(cookie.Value) || !authTimeActive(pending.ExpiresAt, h.authority.now()) {
			return forbidden("login_state_invalid", "login request expired or does not belong to this browser")
		}
		db.Authorizations.Delete(hashToken(state))
		return nil
	})
	http.SetCookie(w, &http.Cookie{Name: loginCookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if err != nil || r.URL.Query().Get("error") != "" || len(r.URL.Query().Get("code")) > 2048 {
		http.Redirect(w, r, "/#auth_error=login_failed", http.StatusSeeOther)
		return
	}
	subject, name, email, err := h.auth.exchange(r.Context(), r.URL.Query().Get("code"), pending.Verifier, pending.Nonce)
	var account contracts.Account
	if err == nil {
		account, err = h.authority.externalAccount(h.auth.Issuer, subject, name, email)
	}
	var response contracts.DashboardSessionResponse
	var token string
	if err == nil {
		response, token, err = h.authority.createDashboardSessionForAccount(account, "")
	}
	if err != nil {
		http.Redirect(w, r, "/#auth_error=login_failed", http.StatusSeeOther)
		return
	}
	setDashboardCookie(w, token, response.ExpiresAt)
	target := "/"
	if pending.AuthorizationID != "" {
		target += "#authorize=" + url.QueryEscape(pending.AuthorizationID)
	} else if pending.ReturnProjectID != "" {
		target += url.PathEscape(pending.ReturnProjectID)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func setDashboardCookie(w http.ResponseWriter, token, expiry string) {
	expires, _ := time.Parse(time.RFC3339Nano, expiry)
	http.SetCookie(w, &http.Cookie{Name: dashboardCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(dashboardSessionTTL.Seconds()), Expires: expires})
}
