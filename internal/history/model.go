package history

import "time"

type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusSuccess   RunStatus = "success"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
	StatusNoChange  RunStatus = "no_change"
)

type PackageStatus string

const (
	PackageStored PackageStatus = "stored"
	PackageFailed PackageStatus = "failed"
)

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
	Packages       []Package  `gorm:"foreignKey:RunID" json:"packages,omitempty"`
}

type Package struct {
	ID           string        `gorm:"type:text;primaryKey" json:"id"`
	RunID        string        `gorm:"type:text;index;not null" json:"run_id"`
	SourceKind   string        `gorm:"type:text;not null" json:"source_kind"`
	SourceName   string        `gorm:"type:text;not null" json:"source_name"`
	Destination  string        `gorm:"type:text;not null" json:"destination"`
	ObjectKey    string        `gorm:"type:text;not null" json:"object_key"`
	Size         int64         `gorm:"not null" json:"size"`
	SHA256       string        `gorm:"type:text;not null" json:"sha256"`
	Status       PackageStatus `gorm:"type:text;not null" json:"status"`
	ErrorMessage string        `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt    time.Time     `gorm:"not null" json:"created_at"`
}

// TableName pins the SQLite table to the pre-rename name so existing history
// databases keep their rows. ponytail: rename the table when a versioned
// migration system exists.
func (Package) TableName() string { return "artifacts" }

type RunFilter struct {
	Site  string
	Limit int
}
