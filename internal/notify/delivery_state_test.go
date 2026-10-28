package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/require"
)

func TestDeliveryStateSurvivesRestartAndRecoversOnlyMatchingScope(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "state.db")
	db, closeDB, err := history.Open(file)
	require.NoError(t, err)
	require.NoError(t, history.Migrate(ctx, db))
	channel := &recordingChannel{name: "test"}
	routes := []config.Route{{Events: []string{config.EventBackupFailed}, Channels: []string{"test"}}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	makeDispatcher := func(repo *history.Repository) *Dispatcher {
		d := NewDispatcher(map[string]Channel{"test": channel}, routes, "server").WithDeliveryState(repo)
		d.now = func() time.Time { return now }
		return d
	}
	d := makeDispatcher(history.NewRepository(db))
	require.NoError(t, d.Notify(ctx, notifyInput(config.EventBackupSkipped)))
	require.Empty(t, channel.payloads)
	input := notifyInput(config.EventBackupFailed)
	require.NoError(t, d.Notify(ctx, input))
	require.Len(t, channel.payloads, 1)
	require.NoError(t, closeDB())
	db, closeDB, err = history.Open(file)
	require.NoError(t, err)
	defer closeDB()
	d = makeDispatcher(history.NewRepository(db))
	require.NoError(t, d.Notify(ctx, input))
	require.Len(t, channel.payloads, 1)
	now = now.Add(24 * time.Hour)
	require.NoError(t, d.Notify(ctx, input))
	require.Len(t, channel.payloads, 2)
	input.ErrorCategory = "storage"
	require.NoError(t, d.Notify(ctx, input))
	require.Len(t, channel.payloads, 3)
	healthy := input
	healthy.Event = config.EventBackupSucceeded
	healthy.Status = backup.StatusSuccess
	healthy.Scope = "database_only"
	require.NoError(t, d.Notify(ctx, healthy))
	require.Len(t, channel.payloads, 3)
	healthy.Scope = "full"
	require.NoError(t, d.Notify(ctx, healthy))
	require.Len(t, channel.payloads, 4)
	require.Equal(t, Event(config.EventBackupSucceeded), channel.payloads[3].Event)
	require.NoError(t, d.Notify(ctx, healthy))
	require.Len(t, channel.payloads, 4)
	channel.err = errors.New("unavailable")
	require.Error(t, d.Notify(ctx, input))
	require.Len(t, channel.payloads, 5)
	channel.err = nil
	require.NoError(t, d.Notify(ctx, input))
	require.Len(t, channel.payloads, 6)
	channel.err = errors.New("recovery failed")
	require.Error(t, d.Notify(ctx, healthy))
	channel.err = nil
	require.NoError(t, d.Notify(ctx, healthy))
	require.Len(t, channel.payloads, 8)
	require.NoError(t, d.Notify(ctx, healthy))
	require.Len(t, channel.payloads, 8)
}

func TestDiscordRecoveryActuallyDeliversAndRetries(t *testing.T) {
	for _, status := range []backup.Status{backup.StatusSuccess, backup.StatusNoChange} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			db, closeDB, err := history.Open(filepath.Join(t.TempDir(), "state.db"))
			require.NoError(t, err)
			defer closeDB()
			require.NoError(t, history.Migrate(ctx, db))
			var requests atomic.Int32
			received := make(chan discordPayload, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body discordPayload
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					w.WriteHeader(400)
					return
				}
				received <- body
				if requests.Add(1) == 2 {
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			d := NewDispatcher(map[string]Channel{"discord": NewDiscord("discord", server.URL)}, []config.Route{{Events: []string{config.EventBackupFailed}, Channels: []string{"discord"}}}, "server").WithDeliveryState(history.NewRepository(db))
			require.NoError(t, d.Notify(ctx, notifyInput(config.EventBackupFailed)))
			<-received
			healthy := notifyInput(config.EventBackupSucceeded)
			healthy.Status = status
			if status == backup.StatusNoChange {
				healthy.Event = config.EventBackupNoChange
			}
			require.Error(t, d.Notify(ctx, healthy))
			<-received
			require.NoError(t, d.Notify(ctx, healthy))
			body := <-received
			require.Contains(t, body.Embeds[0].Title, "Backup recovered")
			require.NoError(t, d.Notify(ctx, healthy))
			require.Equal(t, int32(3), requests.Load())
		})
	}
}
