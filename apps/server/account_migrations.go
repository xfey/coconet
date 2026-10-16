package main

// revokeLegacyTestAccess invalidates credentials issued by the retired trial
// login. Accounts and project ownership remain intact. No new trial login can
// be created; repeating this migration is harmless.
func (a *authority) revokeLegacyTestAccess() error {
	return a.store.update(func(state *databaseState) error {
		accounts := map[string]bool{}
		for _, id := range []string{"D01", "D02", "D03", "D04", "D05"} {
			if account := state.ExternalIdentities.Get(hashToken("coconet:debug" + "\x00" + id)); account != "" {
				accounts[account] = true
			}
		}
		if len(accounts) == 0 {
			return nil
		}
		for key, request := range state.Authorizations.All() {
			if accounts[request.AccountID] || accounts[request.ExpectedAccountID] {
				state.Authorizations.Delete(key)
			}
		}
		for key, token := range state.Tokens.All() {
			if accounts[token.AccountID] {
				state.Tokens.Delete(key)
			}
		}
		for key, session := range state.DashboardSessions.All() {
			if accounts[session.AccountID] {
				state.DashboardSessions.Delete(key)
				state.dashboardAccessChanged = true
			}
		}
		return nil
	})
}
