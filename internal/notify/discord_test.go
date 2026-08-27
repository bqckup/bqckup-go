package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordPostsEmbed(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", "BQCKUP_DISCORD_WEBHOOK_URL", func(string) (string, bool) { return server.URL, true })
	require.NoError(t, discord.Send(context.Background(), NewPayload(notifyInput(config.EventBackupSucceeded))))

	body := <-received
	assert.Equal(t, "Bqckup", body.Username)
	require.Len(t, body.Embeds, 1)
	assert.Equal(t, "[bqckup] backup_succeeded: example.org", body.Embeds[0].Title)
	assert.Equal(t, 0x2ECC71, body.Embeds[0].Color)
}

func TestDiscordEmbedContents(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", "BQCKUP_DISCORD_WEBHOOK_URL", func(string) (string, bool) { return server.URL, true })
	input := notifyInput(config.EventBackupFailed)
	input.Status = backup.StatusFailed
	input.ErrorCategory = "execution"
	input.ErrorMessage = "could not create the file archive"
	require.NoError(t, discord.Send(context.Background(), NewPayload(input)))

	body := <-received
	assert.Equal(t, "Bqckup", body.Username)
	require.Len(t, body.Embeds, 1)
	embed := body.Embeds[0]
	assert.Equal(t, "[bqckup] backup_failed: example.org", embed.Title)
	assert.Equal(t, 0xE74C3C, embed.Color)
	fields := make(map[string]string)
	for _, field := range embed.Fields {
		fields[field.Name] = field.Value
	}
	assert.Equal(t, "example.org", fields["site"])
	assert.Equal(t, "failed", fields["status"])
	assert.Equal(t, "execution", fields["error_category"])
	assert.Equal(t, "could not create the file archive", fields["error_message"])
}

func TestDiscordSuccessEmbedCarriesNoErrorFields(t *testing.T) {
	received := make(chan discordPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body discordPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	discord := NewDiscord("discord", "BQCKUP_DISCORD_WEBHOOK_URL", func(string) (string, bool) { return server.URL, true })
	require.NoError(t, discord.Send(context.Background(), NewPayload(notifyInput(config.EventBackupSucceeded))))

	body := <-received
	require.Len(t, body.Embeds, 1)
	for _, field := range body.Embeds[0].Fields {
		assert.NotEqual(t, "error_message", field.Name)
		assert.NotEqual(t, "error_category", field.Name)
	}
	assert.Equal(t, 0x2ECC71, body.Embeds[0].Color)
}

func TestDiscordReturnsErrorsLikeWebhook(t *testing.T) {
	discord := NewDiscord("discord", "BQCKUP_DISCORD_WEBHOOK_URL", func(string) (string, bool) { return "", false })
	err := discord.Send(context.Background(), NewPayload(notifyInput(config.EventBackupSucceeded)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BQCKUP_DISCORD_WEBHOOK_URL")
}
