package history

import "time"

type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusSuccess   RunStatus = "success"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

type ArtifactStatus string

const (
	ArtifactStored ArtifactStatus = "stored"
	ArtifactFailed ArtifactStatus = "failed"
)

type SchemaMigration struct {
	Version   int       `gorm:"primaryKey;autoIncrement:false"`
	AppliedAt time.Time `gorm:"not null"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }

type BackupRun struct {
	ID             string     `gorm:"type:text;primaryKey" json:"id"`
	SiteName       string     `gorm:"type:text;index;not null" json:"site_name"`
	Status         RunStatus  `gorm:"type:text;index;not null" json:"status"`
	Forced         bool       `gorm:"not null" json:"forced"`
	StartedAt      time.Time  `gorm:"index;not null" json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	DurationMillis int64      `gorm:"not null;default:0" json:"duration_millis"`
	ErrorCategory  string     `gorm:"type:text" json:"error_category,omitempty"`
	ErrorMessage   string     `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt      time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`
	Artifacts      []Artifact `gorm:"foreignKey:RunID" json:"artifacts,omitempty"`
}

type Artifact struct {
	ID           string         `gorm:"type:text;primaryKey" json:"id"`
	RunID        string         `gorm:"type:text;index;not null" json:"run_id"`
	SourceKind   string         `gorm:"type:text;not null" json:"source_kind"`
	SourceName   string         `gorm:"type:text;not null" json:"source_name"`
	Destination  string         `gorm:"type:text;not null" json:"destination"`
	ObjectKey    string         `gorm:"type:text;not null" json:"object_key"`
	Size         int64          `gorm:"not null" json:"size"`
	SHA256       string         `gorm:"type:text;not null" json:"sha256"`
	Status       ArtifactStatus `gorm:"type:text;not null" json:"status"`
	ErrorMessage string         `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt    time.Time      `gorm:"not null" json:"created_at"`
}

type RunFilter struct {
	Site  string
	Limit int
}
