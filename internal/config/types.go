package config

import (
	"sort"
	"time"
)

const SchemaVersion = 2

// Config is the immutable, fully loaded application configuration.
type Config struct {
	Version       int
	ServerID      string
	App           App
	Storages      map[string]Storage
	Sites         []Site
	Notifications Notifications
	Reports       Reports
}

// Notification event names. These are the canonical values for the
// notifications route contract; internal/notify maps them to its typed enum.
const (
	EventAll             = "all"
	EventBackupFailed    = "backup_failed"
	EventBackupCancelled = "backup_cancelled"
	EventBackupNoChange  = "backup_no_change"
	EventDailyReport     = "daily_report"
	EventMonthlyReport   = "monthly_report"
)

// Notifications is the optional top-level notifications section. Absent in
// YAML it stays the zero value and notifications are off.
type Notifications struct {
	Channels map[string]Channel `mapstructure:"channels" yaml:"channels"`
	Routes   []Route            `mapstructure:"routes" yaml:"routes"`
}

// Channel is one configured delivery channel. All fields share one struct;
// validation enforces the per-type allowed field set, so fields foreign to a
// channel type (for example host on a webhook channel) are rejected.
// Notification credentials and URLs are literal values loaded from the
// protected root configuration file.
type Channel struct {
	Type       string   `mapstructure:"type" yaml:"type"`
	Host       string   `mapstructure:"host" yaml:"host"`
	Port       int      `mapstructure:"port" yaml:"port"`
	Username   string   `mapstructure:"username" yaml:"username"`
	Password   string   `mapstructure:"password" yaml:"password"`
	From       string   `mapstructure:"from" yaml:"from"`
	To         []string `mapstructure:"to" yaml:"to"`
	URL        string   `mapstructure:"url" yaml:"url"`
	WebhookURL string   `mapstructure:"webhook_url" yaml:"webhook_url"`
}

// Route maps one or more events to one or more channels.
type Route struct {
	Name     string   `mapstructure:"name" yaml:"name"`
	Events   []string `mapstructure:"events" yaml:"events"`
	Channels []string `mapstructure:"channels" yaml:"channels"`
}

// Reports is the optional top-level reports section. Absent in YAML it stays
// the zero value and scheduled reports are off.
type Reports struct {
	Daily   DailyReport   `mapstructure:"daily" yaml:"daily"`
	Monthly MonthlyReport `mapstructure:"monthly" yaml:"monthly"`
}

// DailyReport configures the daily backup summary report.
type DailyReport struct {
	Enabled           bool           `mapstructure:"enabled" yaml:"enabled"`
	Timezone          string         `mapstructure:"timezone" yaml:"timezone"`
	Schedule          ReportSchedule `mapstructure:"schedule" yaml:"schedule"`
	NotificationRoute string         `mapstructure:"notification_route" yaml:"notification_route"`
	IncludeEmptyDays  bool           `mapstructure:"include_empty_days" yaml:"include_empty_days"`
}

// MonthlyReport configures the monthly consolidated backup report.
type MonthlyReport struct {
	Enabled           bool           `mapstructure:"enabled" yaml:"enabled"`
	Timezone          string         `mapstructure:"timezone" yaml:"timezone"`
	Schedule          ReportSchedule `mapstructure:"schedule" yaml:"schedule"`
	NotificationRoute string         `mapstructure:"notification_route" yaml:"notification_route"`
	IncludeEmptyDays  bool           `mapstructure:"include_empty_days" yaml:"include_empty_days"`
}

// ReportSchedule defines when a report is sent.
type ReportSchedule struct {
	// DayOfMonth is only used by monthly reports (1–28).
	DayOfMonth int    `mapstructure:"day_of_month" yaml:"day_of_month"`
	Time       string `mapstructure:"time" yaml:"time"`
}

type App struct {
	StateDatabase      string `mapstructure:"state_database" yaml:"state_database"`
	TemporaryDirectory string `mapstructure:"temporary_directory" yaml:"temporary_directory"`
	LockDirectory      string `mapstructure:"lock_directory" yaml:"lock_directory"`
	LogLevel           string `mapstructure:"log_level" yaml:"log_level"`
	LogFile            string `mapstructure:"log_file" yaml:"log_file"`
}

type Storage struct {
	Type            string             `mapstructure:"type" yaml:"type"`
	Directory       string             `mapstructure:"directory" yaml:"directory"`
	Bucket          string             `mapstructure:"bucket" yaml:"bucket"`
	AccessKeyID     string             `mapstructure:"access_key_id" yaml:"access_key_id"`
	SecretAccessKey string             `mapstructure:"secret_access_key" yaml:"secret_access_key"`
	Region          string             `mapstructure:"region" yaml:"region"`
	Endpoint        string             `mapstructure:"endpoint" yaml:"endpoint"`
	Prefix          string             `mapstructure:"prefix" yaml:"prefix"`
	Primary         bool               `mapstructure:"primary" yaml:"primary"`
	Credentials     StorageCredentials `mapstructure:"credentials" yaml:"credentials"`
}

type StorageCredentials struct {
	Source string `mapstructure:"source" yaml:"source"`
	URL    string `mapstructure:"url" yaml:"url"`
}

type Site struct {
	SchemaVersion int           `mapstructure:"-" yaml:"-"`
	SourceFile    string        `mapstructure:"-" yaml:"-"`
	Name          string        `mapstructure:"name" yaml:"name"`
	Enabled       bool          `mapstructure:"enabled" yaml:"enabled"`
	BackupMode    string        `mapstructure:"backup_mode" yaml:"backup_mode"`
	Incremental   Incremental   `mapstructure:"incremental" yaml:"incremental"`
	Sources       Sources       `mapstructure:"sources" yaml:"sources"`
	Destinations  []Destination `mapstructure:"destinations" yaml:"destinations"`
	Policy        Policy        `mapstructure:"policy" yaml:"policy"`
}

type Incremental struct {
	Password string `mapstructure:"password" yaml:"password"`
}

type Sources struct {
	Files     FileSource       `mapstructure:"files" yaml:"files"`
	Databases []DatabaseSource `mapstructure:"databases" yaml:"databases"`
}

type FileSource struct {
	Include        []string `mapstructure:"include" yaml:"include"`
	Exclude        []string `mapstructure:"exclude" yaml:"exclude"`
	FollowSymlinks bool     `mapstructure:"follow_symlinks" yaml:"follow_symlinks"`
}

type DatabaseSource struct {
	Name     string `mapstructure:"name" yaml:"name"`
	Enabled  bool   `mapstructure:"enabled" yaml:"enabled"`
	Engine   string `mapstructure:"engine" yaml:"engine"`
	Host     string `mapstructure:"host" yaml:"host"`
	Port     int    `mapstructure:"port" yaml:"port"`
	Database string `mapstructure:"database" yaml:"database"`
	Username string `mapstructure:"username" yaml:"username"`
	Password string `mapstructure:"password" yaml:"password"`
}

type Destination struct {
	Storage string `mapstructure:"storage" yaml:"storage"`
}

type Policy struct {
	MinimumInterval time.Duration `mapstructure:"minimum_interval" yaml:"minimum_interval"`
	KeepLast        int           `mapstructure:"keep_last" yaml:"keep_last"`
}

func (c Config) Site(name string) (Site, bool) {
	for _, site := range c.Sites {
		if site.Name == name {
			return site, true
		}
	}
	return Site{}, false
}

// PrimaryStorage returns the configured primary storage when exactly one exists.
func (c Config) PrimaryStorage() (string, bool) {
	names := make([]string, 0, len(c.Storages))
	for name, storage := range c.Storages {
		if storage.Primary {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) != 1 {
		return "", false
	}
	return names[0], true
}
