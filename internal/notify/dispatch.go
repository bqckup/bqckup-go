package notify

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/bqckup/bqckup-go/internal/history"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
)

// Channel is one deliverable notification channel. Implementations live in
// this package; the interface exists so the dispatcher stays independent of
// the transport.
type Channel interface {
	Name() string
	Send(ctx context.Context, payload Payload) error
}

// Dispatcher implements backup.Notifier: it builds the shared payload and
// sends it through every channel whose route matches the event. Sends are
// sequential; a failing channel never stops the others and never changes the
// run outcome. A channel matched through several routes is sent once.
type DeliveryState interface {
	LoadNotification(context.Context, string) (history.NotificationState, error)
	SaveNotification(context.Context, history.NotificationState) error
}

type Dispatcher struct {
	state    DeliveryState
	now      func() time.Time
	channels map[string]Channel
	routes   []config.Route
	serverID string
	hostname string
	serverIP string
}

func NewDispatcher(channels map[string]Channel, routes []config.Route, serverID string) *Dispatcher {
	hostname, serverIP := serverIdentity()
	return &Dispatcher{channels: channels, routes: routes, serverID: serverID, hostname: hostname, serverIP: serverIP, now: time.Now}
}

func (d *Dispatcher) WithDeliveryState(state DeliveryState) *Dispatcher { d.state = state; return d }

func (d *Dispatcher) Notify(ctx context.Context, input backup.NotifyInput) error {
	payload := NewPayload(input)
	payload.ServerID = d.serverID
	payload.Hostname = d.hostname
	payload.ServerIP = d.serverIP

	var errs []error
	channels := d.channelsFor(input.Event)
	healthy := input.Event == config.EventBackupSucceeded || input.Event == config.EventBackupNoChange
	problem := input.Event == config.EventBackupFailed || input.Event == config.EventBackupPartial || input.Event == config.EventBackupCancelled
	if d.state != nil && healthy {
		seen := map[string]bool{}
		for _, channel := range channels {
			seen[channel.Name()] = true
		}
		for _, event := range []string{config.EventBackupFailed, config.EventBackupPartial, config.EventBackupCancelled} {
			for _, channel := range d.channelsFor(event) {
				if _, webhook := channel.(*Webhook); webhook {
					continue
				}
				if !seen[channel.Name()] {
					channels = append(channels, channel)
					seen[channel.Name()] = true
				}
			}
		}
	}
	for _, channel := range channels {
		_, webhook := channel.(*Webhook)
		if d.state != nil && input.Event == config.EventBackupSkipped && !webhook {
			continue
		}
		delivery := payload
		var state history.NotificationState
		if d.state != nil && !webhook && (healthy || problem) {
			scope := input.Scope
			if scope == "" {
				scope = string(history.RunScopeFull)
			}
			key := fmt.Sprintf("%x", sha256.Sum256([]byte(d.serverID+"\x00"+input.SiteName+"\x00"+scope+"\x00"+channel.Name())))
			var err error
			state, err = d.state.LoadNotification(ctx, key)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: load delivery state: %w", channel.Name(), err))
				state = history.NotificationState{Key: key}
			}
			if healthy {
				if state.Fingerprint == "" {
					continue
				}
				delivery.Event = Event(config.EventBackupSucceeded)
				delivery.Recovery = true
				delivery.ErrorMessage = "Backup recovered"
				delivery.ErrorCategory = ""
				delivery.FailureStreak = 0
				state.Fingerprint = ""
			} else {
				fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(input.Event+"\x00"+input.ErrorCategory+"\x00"+input.ErrorMessage)))
				if state.Fingerprint == fingerprint && d.now().Sub(state.LastSent) < 24*time.Hour {
					continue
				}
				state.Fingerprint = fingerprint
			}
		}
		if err := channel.Send(ctx, delivery); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", channel.Name(), err))
			continue
		}
		if d.state != nil && !webhook && (healthy || problem) {
			state.LastSent = d.now()
			if err := d.state.SaveNotification(ctx, state); err != nil {
				errs = append(errs, fmt.Errorf("%s: save delivery state: %w", channel.Name(), err))
			}
		}
	}

	return errors.Join(errs...)
}

// channelsFor returns the matching channels for one event in route order,
// deduplicated by name. Unknown channel names are skipped: config validation
// already rejects them, this is defense for miswired dispatchers.
func (d *Dispatcher) channelsFor(event string) []Channel {
	seen := make(map[string]struct{})
	var matched []Channel
	for _, route := range d.routes {
		if !containsString(route.Events, event) && !containsString(route.Events, config.EventAll) {
			continue
		}
		for _, name := range route.Channels {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			if channel, ok := d.channels[name]; ok {
				matched = append(matched, channel)
			}
		}
	}
	return matched
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
