package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Content capabilities must not outlive membership or the
// device credential which requested them. Already issued provider URLs have
// their own short expiry; this checks that no new capability can be obtained.
func TestRelayReplicaRoutesRecheckMembershipAndDevice(t *testing.T) {
	for _, revoke := range []string{"membership", "device"} {
		t.Run(revoke, func(t *testing.T) {
			a, handler, actor, project, _ := dashboardFixture(t)
			device := authorizeTestDevice(t, a, actor, "Replica acceptance")
			other, _ := issueTestIdentity(t, a, "Other", "replica-other@coconet.test")
			outsider := authorizeTestDevice(t, a, other, "Outside project")
			routes := []struct{ method, path string }{
				{"GET", "content-policy"}, {"GET", "checkpoint-head"},
				{"GET", "repairs"},
				{"POST", "repairs/request"}, {"POST", "repairs/claim"},
				{"POST", "transfers/upload"}, {"POST", "transfers/complete"}, {"POST", "transfers/download"},
			}
			invoke := func(method, path, token string) int {
				r := httptest.NewRequest(method, "/v1/projects/"+project.ProjectID+"/relay/"+path, strings.NewReader("{"))
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w.Code
			}
			for _, route := range routes {
				want := 400 // Deliberately malformed input is decoded only after authorization.
				if route.method == "GET" {
					want = 200
				}
				if got := invoke(route.method, route.path, device.AccessToken); got != want {
					t.Fatalf("active %s %s: got %d, want %d", route.method, route.path, got, want)
				}
				for _, denied := range []struct {
					token string
					code  int
				}{{"", 401}, {outsider.AccessToken, 403}} {
					if got := invoke(route.method, route.path, denied.token); got != denied.code {
						t.Fatalf("unauthorized %s %s: got %d, want %d", route.method, route.path, got, denied.code)
					}
				}
			}
			want := 403
			if revoke == "membership" {
				if err := a.store.update(func(state *databaseState) error {
					state.Memberships.Delete(membershipKey(project.ProjectID, actor.AccountID))
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				devices, err := a.accountDevices(actor)
				if err != nil || len(devices.Devices) != 1 {
					t.Fatal("expected one isolated device", err)
				}
				if err := a.revokeDevice(actor, devices.Devices[0].ID); err != nil {
					t.Fatal(err)
				}
				want = 401
			}
			for _, route := range routes {
				if got := invoke(route.method, route.path, device.AccessToken); got != want {
					t.Fatalf("revoked %s %s: got %d, want %d", route.method, route.path, got, want)
				}
			}
		})
	}
}
