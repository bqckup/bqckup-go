package main

import (
  "fmt"
  "time"

  "github.com/bqckup/bqckup-go/internal/history"
  "gorm.io/driver/sqlite"
  "gorm.io/gorm"
)

func ptr(t time.Time) *time.Time { return &t }

func main() {
  db, err := gorm.Open(sqlite.Open("/mnt/workspace/Intern/fix_issue/bqckup-go/tmp-demo/data/bqckup.db"), &gorm.Config{})
  if err != nil { panic(err) }
  if err := db.AutoMigrate(&history.BackupRun{}, &history.Package{}, &history.ReportDelivery{}); err != nil { panic(err) }

  if err := db.Exec("DELETE FROM artifacts").Error; err != nil { panic(err) }
  if err := db.Exec("DELETE FROM backup_runs").Error; err != nil { panic(err) }
  if err := db.Exec("DELETE FROM report_deliveries").Error; err != nil { panic(err) }

  now := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
  runs := []history.BackupRun{
    {
      ID:        "r1",
      SiteName:  "demo-prod",
      Status:    history.StatusSuccess,
      StartedAt: now.Add(-2 * time.Hour),
      FinishedAt: ptr(now.Add(-1 * time.Hour)),
      DurationMillis: 1800000,
      Packages: []history.Package{{
        ID:          "p1",
        RunID:       "r1",
        SourceKind:  "files",
        SourceName:  "files",
        Destination: "local-main",
        ObjectKey:   "demo-prod/file1.tar",
        Size:        1024,
        SHA256:      "a",
        Status:      history.PackageStored,
      }},
    },
    {
      ID:        "r2",
      SiteName:  "demo-prod",
      Status:    history.StatusFailed,
      StartedAt: now.Add(-1 * time.Hour),
      FinishedAt: ptr(now.Add(-45 * time.Minute)),
      DurationMillis: 900000,
      ErrorCategory: "storage",
      ErrorMessage: "timeout",
    },
    {
      ID:        "r3",
      SiteName:  "demo-staging",
      Status:    history.StatusSuccess,
      StartedAt: now.Add(-30 * time.Minute),
      FinishedAt: ptr(now.Add(-20 * time.Minute)),
      DurationMillis: 600000,
      Packages: []history.Package{{
        ID:          "p2",
        RunID:       "r3",
        SourceKind:  "files",
        SourceName:  "files",
        Destination: "local-main",
        ObjectKey:   "demo-staging/file2.tar",
        Size:        2048,
        SHA256:      "b",
        Status:      history.PackageStored,
      }},
    },
  }

  for i := range runs {
    if err := db.Create(&runs[i]).Error; err != nil { panic(err) }
  }
  fmt.Println("seeded demo report data")
}
