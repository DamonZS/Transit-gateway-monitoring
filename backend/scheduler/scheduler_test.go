package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/monitor"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

type fakeGatewayResort struct {
	called int
}

func (f *fakeGatewayResort) ResortRoutesOnRateScan(_ context.Context) {
	f.called++
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "scheduler-test.db"),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestRunRetentionDeletesAnnouncements(t *testing.T) {
	db := openTestDB(t)
	announcements := storage.NewUpstreamAnnouncements(db)
	notifies := storage.NewNotifications(db)
	monLogs := storage.NewMonitorLogs(db)
	rates := storage.NewRates(db)

	oldTime := time.Now().AddDate(0, 0, -10)
	if _, err := announcements.Sync(1, []storage.UpstreamAnnouncement{{
		ChannelID:   1,
		SourceKey:   "old",
		Content:     "old",
		FirstSeenAt: oldTime,
	}}); err != nil {
		t.Fatalf("sync announcement: %v", err)
	}

	s := New(
		config.SchedulerConfig{
			Retention: config.RetentionConfig{
				AnnouncementsDays: 1,
			},
		},
		&monitor.Service{},
		monLogs,
		rates,
		notifies,
		announcements,
		nil,
		nil,
		nil,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRetention()

	list, total, err := announcements.ListPage(1, 10)
	if err != nil {
		t.Fatalf("list announcements: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("announcements not cleaned: total=%d list=%#v", total, list)
	}
}

func TestRunRatesTriggersGatewayResort(t *testing.T) {
	gatewayResort := &fakeGatewayResort{}
	s := New(
		config.SchedulerConfig{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		gatewayResort,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRates()

	if gatewayResort.called != 1 {
		t.Fatalf("gateway resort calls = %d, want 1", gatewayResort.called)
	}
}
