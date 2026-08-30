package notify

import (
	"context"
	"errors"
	"fmt"

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
type Dispatcher struct {
	channels map[string]Channel
	routes   []config.Route
	hostname string
	serverIP string
}

func NewDispatcher(channels map[string]Channel, routes []config.Route) *Dispatcher {
	hostname, serverIP := serverIdentity()
	return &Dispatcher{channels: channels, routes: routes, hostname: hostname, serverIP: serverIP}
}

func (d *Dispatcher) Notify(ctx context.Context, input backup.NotifyInput) error {
	payload := NewPayload(input)
	payload.Hostname = d.hostname
	payload.ServerIP = d.serverIP
	var errs []error
	for _, channel := range d.channelsFor(input.Event) {
		if err := channel.Send(ctx, payload); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", channel.Name(), err))
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
