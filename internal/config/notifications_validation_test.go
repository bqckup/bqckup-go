package config

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateNotificationsRejectsInvalidForms(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Notifications)
		wantErr string
	}{
		{
			name:    "empty channels",
			mutate:  func(n *Notifications) { n.Channels = nil },
			wantErr: "notifications.channels",
		},
		{
			name:    "empty routes",
			mutate:  func(n *Notifications) { n.Routes = nil },
			wantErr: "notifications.routes",
		},
		{
			name: "unknown channel type",
			mutate: func(n *Notifications) {
				n.Channels["email"] = Channel{Type: "telegram"}
			},
			wantErr: "type must be one of smtp, webhook, or discord",
		},
		{
			name: "smtp without host",
			mutate: func(n *Notifications) {
				n.Channels["email"] = Channel{Type: "smtp", Port: 587, From: "bqckup@example.com", To: []string{"ops@example.com"}}
			},
			wantErr: "host is required",
		},
		{
			name: "smtp without port",
			mutate: func(n *Notifications) {
				n.Channels["email"] = Channel{Type: "smtp", Host: "smtp.example.com", From: "bqckup@example.com", To: []string{"ops@example.com"}}
			},
			wantErr: "port is required",
		},
		{
			name: "smtp port out of range",
			mutate: func(n *Notifications) {
				n.Channels["email"] = Channel{Type: "smtp", Host: "smtp.example.com", Port: 70000, From: "bqckup@example.com", To: []string{"ops@example.com"}}
			},
			wantErr: "must be between 1 and 65535",
		},
		{
			name: "smtp without from",
			mutate: func(n *Notifications) {
				n.Channels["email"] = Channel{Type: "smtp", Host: "smtp.example.com", Port: 587, To: []string{"ops@example.com"}}
			},
			wantErr: "from is required",
		},
		{
			name: "smtp without recipients",
			mutate: func(n *Notifications) {
				n.Channels["email"] = Channel{Type: "smtp", Host: "smtp.example.com", Port: 587, From: "bqckup@example.com"}
			},
			wantErr: "at least one recipient",
		},
		{
			name: "webhook without url",
			mutate: func(n *Notifications) {
				n.Channels["webhook"] = Channel{Type: "webhook"}
			},
			wantErr: "url is required",
		},
		{
			name: "discord without webhook_url",
			mutate: func(n *Notifications) {
				n.Channels["discord"] = Channel{Type: "discord"}
			},
			wantErr: "webhook_url is required",
		},
		{
			name: "smtp with url",
			mutate: func(n *Notifications) {
				channel := n.Channels["email"]
				channel.URL = "BQCKUP_WEBHOOK_URL"
				setChannel(n, "email", channel)
			},
			wantErr: "url is not valid for smtp channels",
		},
		{
			name: "webhook with host",
			mutate: func(n *Notifications) {
				channel := n.Channels["webhook"]
				channel.Host = "smtp.example.com"
				setChannel(n, "webhook", channel)
			},
			wantErr: "host is not valid for webhook channels",
		},
		{
			name: "webhook with webhook_url",
			mutate: func(n *Notifications) {
				channel := n.Channels["webhook"]
				channel.WebhookURL = "BQCKUP_DISCORD_WEBHOOK_URL"
				setChannel(n, "webhook", channel)
			},
			wantErr: "webhook_url is not valid for webhook channels",
		},
		{
			name: "discord with url",
			mutate: func(n *Notifications) {
				channel := n.Channels["discord"]
				channel.URL = "BQCKUP_WEBHOOK_URL"
				setChannel(n, "discord", channel)
			},
			wantErr: "url is not valid for discord channels",
		},
		{
			name: "username without password",
			mutate: func(n *Notifications) {
				channel := n.Channels["email"]
				channel.Username = "BQCKUP_SMTP_USERNAME"
				channel.Password = ""
				setChannel(n, "email", channel)
			},
			wantErr: "must be provided together",
		},
		{
			name: "password without username",
			mutate: func(n *Notifications) {
				channel := n.Channels["email"]
				channel.Password = "BQCKUP_SMTP_PASSWORD"
				channel.Username = ""
				setChannel(n, "email", channel)
			},
			wantErr: "must be provided together",
		},
		{
			name: "invalid env name",
			mutate: func(n *Notifications) {
				channel := n.Channels["webhook"]
				channel.URL = "webhook-url"
				setChannel(n, "webhook", channel)
			},
			wantErr: "must be a valid environment variable name",
		},
		{
			name: "unsafe channel name",
			mutate: func(n *Notifications) {
				n.Channels["Bad Name"] = n.Channels["email"]
				delete(n.Channels, "email")
			},
			wantErr: "name contains unsupported characters",
		},
		{
			name: "route references unknown channel",
			mutate: func(n *Notifications) {
				n.Routes[0].Channels = []string{"pagerduty"}
			},
			wantErr: "references unknown channel",
		},
		{
			name: "route with empty events",
			mutate: func(n *Notifications) {
				n.Routes[0].Events = nil
			},
			wantErr: "at least one event is required",
		},
		{
			name: "route with unknown event",
			mutate: func(n *Notifications) {
				n.Routes[0].Events = []string{"backup_started"}
			},
			wantErr: "must be one of backup_failed, backup_cancelled, or backup_no_change",
		},
		{
			name: "route with backup_succeeded rejected",
			mutate: func(n *Notifications) {
				n.Routes[0].Events = []string{"backup_succeeded"}
			},
			wantErr: "must be one of backup_failed, backup_cancelled, or backup_no_change",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.Notifications = validNotifications()
			test.mutate(&cfg.Notifications)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
			assert.Contains(t, err.Error(), "notifications")
		})
	}
}

func TestValidateNotificationsAcceptsValidForms(t *testing.T) {
	cfg := validConfig(t)
	cfg.Notifications = validNotifications()
	require.NoError(t, cfg.Validate())

	// Route with backup_no_change is valid.
	cfg.Notifications.Routes = append(cfg.Notifications.Routes, Route{
		Events:   []string{EventBackupNoChange},
		Channels: []string{"email"},
	})
	require.NoError(t, cfg.Validate())

	// Unauthenticated SMTP is allowed.
	cfg.Notifications.Channels["email"] = Channel{
		Type: "smtp", Host: "smtp.example.com", Port: 25,
		From: "bqckup@example.com", To: []string{"ops@example.com"},
	}
	require.NoError(t, cfg.Validate())

	// Absent section is the zero value and stays valid.
	cfg.Notifications = Notifications{}
	require.NoError(t, cfg.Validate())
}

// setChannel replaces one channel in the map (map values are not addressable).
func setChannel(n *Notifications, name string, channel Channel) {
	n.Channels[name] = channel
}

func validNotifications() Notifications {
	return Notifications{
		Channels: map[string]Channel{
			"email": {
				Type: "smtp", Host: "smtp.example.com", Port: 587,
				Username: "BQCKUP_SMTP_USERNAME", Password: "BQCKUP_SMTP_PASSWORD",
				From: "bqckup@example.com", To: []string{"ops@example.com"},
			},
			"webhook": {Type: "webhook", URL: "BQCKUP_WEBHOOK_URL"},
			"discord": {Type: "discord", WebhookURL: "BQCKUP_DISCORD_WEBHOOK_URL"},
		},
		Routes: []Route{
			{Events: []string{EventBackupFailed, EventBackupCancelled}, Channels: []string{"email", "discord", "webhook"}},
		},
	}
}

const validNotificationsYAML = `notifications:
  channels:
    email:
      type: smtp
      host: smtp.example.com
      port: 587
      username: BQCKUP_SMTP_USERNAME
      password: BQCKUP_SMTP_PASSWORD
      from: bqckup@example.com
      to:
        - ops@example.com
    webhook:
      type: webhook
      url: BQCKUP_WEBHOOK_URL
    discord:
      type: discord
      webhook_url: BQCKUP_DISCORD_WEBHOOK_URL
  routes:
    - events: [backup_failed, backup_cancelled]
      channels: [email, discord, webhook]
`

func TestLoadDecodesNotificationsSection(t *testing.T) {
	dir := writeConfigTree(t, "version: 2\napp:\n  state_database: data/bqckup.db\n  temporary_directory: tmp\n  lock_directory: locks\n"+validNotificationsYAML, localStorageYAML, validSiteYAML(t))

	cfg, err := Load(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, cfg.Notifications.Channels, 3)
	assert.Equal(t, "smtp", cfg.Notifications.Channels["email"].Type)
	assert.Equal(t, "smtp.example.com", cfg.Notifications.Channels["email"].Host)
	assert.Equal(t, 587, cfg.Notifications.Channels["email"].Port)
	assert.Equal(t, []string{"ops@example.com"}, cfg.Notifications.Channels["email"].To)
	assert.Equal(t, "BQCKUP_WEBHOOK_URL", cfg.Notifications.Channels["webhook"].URL)
	require.Len(t, cfg.Notifications.Routes, 1)
	assert.Equal(t, []string{EventBackupFailed, EventBackupCancelled}, cfg.Notifications.Routes[0].Events)
}

func TestLoadRejectsPlaintextNotificationCredentials(t *testing.T) {
	plaintext := []string{
		"    email:\n      type: smtp\n      host: smtp.example.com\n      port: 587\n      password: hunter2\n      from: bqckup@example.com\n      to: [ops@example.com]\n",
		"    webhook:\n      type: webhook\n      url: https://example.invalid/hook?token=secret\n",
		"    discord:\n      type: discord\n      webhook_url: https://discord.invalid/api/webhooks/1/secret\n",
	}
	for _, channel := range plaintext {
		t.Run(strings.TrimSpace(channel), func(t *testing.T) {
			dir := writeConfigTree(t, "version: 2\napp:\n  state_database: data/bqckup.db\n  temporary_directory: tmp\n  lock_directory: locks\nnotifications:\n  channels:\n"+channel, localStorageYAML, validSiteYAML(t))

			_, err := Load(context.Background(), dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "bqckup.yaml")
		})
	}
}
