package config

import "time"

const SchemaVersion = 2

// Config is the immutable, fully loaded application configuration.
type Config struct {
	Version        int
	StorageVersion int
	App            App
	Storages       map[string]Storage
	Sites          []Site
}

type App struct {
	StateDatabase      string `mapstructure:"state_database" yaml:"state_database"`
	TemporaryDirectory string `mapstructure:"temporary_directory" yaml:"temporary_directory"`
	LockDirectory      string `mapstructure:"lock_directory" yaml:"lock_directory"`
	LogLevel           string `mapstructure:"log_level" yaml:"log_level"`
}

type Storage struct {
	Type        string           `mapstructure:"type" yaml:"type"`
	Directory   string           `mapstructure:"directory" yaml:"directory"`
	Bucket      string           `mapstructure:"bucket" yaml:"bucket"`
	Region      string           `mapstructure:"region" yaml:"region"`
	Endpoint    string           `mapstructure:"endpoint" yaml:"endpoint"`
	Prefix      string           `mapstructure:"prefix" yaml:"prefix"`
	Credentials CredentialConfig `mapstructure:"credentials" yaml:"credentials"`
}

type CredentialConfig struct {
	Source       string `mapstructure:"source" yaml:"source"`
	AccessKeyEnv string `mapstructure:"access_key_env" yaml:"access_key_env"`
	SecretKeyEnv string `mapstructure:"secret_key_env" yaml:"secret_key_env"`
	URLEnv       string `mapstructure:"url_env" yaml:"url_env"`
}

type Site struct {
	SchemaVersion int           `mapstructure:"-" yaml:"-"`
	SourceFile    string        `mapstructure:"-" yaml:"-"`
	Name          string        `mapstructure:"name" yaml:"name"`
	Enabled       bool          `mapstructure:"enabled" yaml:"enabled"`
	Sources       Sources       `mapstructure:"sources" yaml:"sources"`
	Destinations  []Destination `mapstructure:"destinations" yaml:"destinations"`
	Policy        Policy        `mapstructure:"policy" yaml:"policy"`
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
	Name        string `mapstructure:"name" yaml:"name"`
	Enabled     bool   `mapstructure:"enabled" yaml:"enabled"`
	Engine      string `mapstructure:"engine" yaml:"engine"`
	Host        string `mapstructure:"host" yaml:"host"`
	Port        int    `mapstructure:"port" yaml:"port"`
	Database    string `mapstructure:"database" yaml:"database"`
	Username    string `mapstructure:"username" yaml:"username"`
	PasswordEnv string `mapstructure:"password_env" yaml:"password_env"`
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
