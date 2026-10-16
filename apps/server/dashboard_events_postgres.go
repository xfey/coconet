package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

const dashboardNotifyChannel = "coconet_dashboard"

type dashboardNotification struct {
	Projects map[string]bool `json:"projects,omitempty"`
	Access   bool            `json:"access,omitempty"`
}

// LISTEN is a wake-up mechanism, not durable delivery. Reconnecting a listener
// invalidates its subscribers so each checks the current persisted revision.
func (s *store) startDashboardListener(databaseURL string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.eventCancel, s.eventDone = cancel, make(chan struct{})
	go func() {
		defer close(s.eventDone)
		for ctx.Err() == nil {
			connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			connection, err := pgx.Connect(connectCtx, databaseURL)
			if err == nil {
				_, err = connection.Exec(connectCtx, "LISTEN "+dashboardNotifyChannel)
			}
			cancel()
			if err == nil {
				s.events.publish(nil, true)
				for ctx.Err() == nil {
					notification, err := connection.WaitForNotification(ctx)
					if err != nil {
						break
					}
					var event dashboardNotification
					if json.Unmarshal([]byte(notification.Payload), &event) == nil {
						s.events.publish(event.Projects, event.Access)
					}
				}
			}
			if connection != nil {
				closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = connection.Close(closeCtx)
				cancel()
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
