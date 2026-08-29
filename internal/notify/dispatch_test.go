package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingChannel struct {
	name     string
	err      error
	payloads []Payload
}

func (c *recordingChannel) Name() string { return c.name }

func (c *recordingChannel) Send(_ context.Context, payload Payload) error {
	c.payloads = append(c.payloads, payload)
	return c.err
}

func notifyInput(event string) backup.NotifyInput {
	return backup.NotifyInput{
		Event:         event,
		SiteName:      "example.org",
		Status:        backup.StatusFailed,
		ErrorCategory: "execution",
		ErrorMessage:  "something went wrong",
	}
}

func TestDispatcherFansOutToEveryMatchingChannel(t *testing.T) {
	webhook := &recordingChannel{name: "webhook"}
	email := &recordingChannel{name: "email"}
	discord := &recordingChannel{name: "discord"}
	dispatcher := NewDispatcher(map[string]Channel{
		"webhook": webhook, "email": email, "discord": discord,
	}, []config.Route{
		{Events: []string{config.EventBackupFailed}, Channels: []string{"email", "discord"}},
		{Events: []string{config.EventBackupCancelled}, Channels: []string{"webhook"}},
	})

	require.NoError(t, dispatcher.Notify(context.Background(), notifyInput(config.EventBackupFailed)))
	require.Len(t, email.payloads, 1)
	require.Len(t, discord.payloads, 1)
	assert.Empty(t, webhook.payloads)
	assert.Equal(t, EventBackupFailed, email.payloads[0].Event)
	assert.NotEmpty(t, email.payloads[0].Hostname, "payloads must carry the machine hostname")

	cancelledInput := notifyInput(config.EventBackupCancelled)
	cancelledInput.Status = backup.StatusCancelled
	cancelledInput.ErrorCategory = "cancellation"
	cancelledInput.ErrorMessage = "backup was cancelled"
	require.NoError(t, dispatcher.Notify(context.Background(), cancelledInput))
	require.Len(t, webhook.payloads, 1)
	assert.Equal(t, "example.org", webhook.payloads[0].Site)
}

func TestDispatcherUnmatchedEventSendsNothing(t *testing.T) {
	email := &recordingChannel{name: "email"}
	dispatcher := NewDispatcher(map[string]Channel{"email": email}, []config.Route{
		{Events: []string{config.EventBackupFailed}, Channels: []string{"email"}},
	})

	unmatchedInput := notifyInput(config.EventBackupCancelled)
	unmatchedInput.Status = backup.StatusCancelled
	require.NoError(t, dispatcher.Notify(context.Background(), unmatchedInput))
	assert.Empty(t, email.payloads)
}

func TestDispatcherSendsChannelOnceAcrossRoutes(t *testing.T) {
	email := &recordingChannel{name: "email"}
	dispatcher := NewDispatcher(map[string]Channel{"email": email}, []config.Route{
		{Events: []string{config.EventBackupFailed}, Channels: []string{"email"}},
		{Events: []string{config.EventBackupFailed}, Channels: []string{"email"}},
	})

	require.NoError(t, dispatcher.Notify(context.Background(), notifyInput(config.EventBackupFailed)))
	require.Len(t, email.payloads, 1)
}

func TestDispatcherOneFailureDoesNotStopOtherChannels(t *testing.T) {
	broken := &recordingChannel{name: "broken", err: errors.New("down")}
	email := &recordingChannel{name: "email"}
	dispatcher := NewDispatcher(map[string]Channel{"broken": broken, "email": email}, []config.Route{
		{Events: []string{config.EventBackupFailed}, Channels: []string{"broken", "email"}},
	})

	err := dispatcher.Notify(context.Background(), notifyInput(config.EventBackupFailed))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
	require.Len(t, email.payloads, 1, "the other channel must still be attempted")
}

func TestDispatcherSkipsUnknownChannelDefensively(t *testing.T) {
	dispatcher := NewDispatcher(map[string]Channel{}, []config.Route{
		{Events: []string{config.EventBackupFailed}, Channels: []string{"missing"}},
	})

	require.NoError(t, dispatcher.Notify(context.Background(), notifyInput(config.EventBackupFailed)))
}

func TestDispatcherWithoutChannelsSendsNothing(t *testing.T) {
	dispatcher := NewDispatcher(nil, nil)
	require.NoError(t, dispatcher.Notify(context.Background(), notifyInput(config.EventBackupFailed)))
}
