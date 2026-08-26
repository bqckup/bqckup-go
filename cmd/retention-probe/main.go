//go:build bqckupprobe

// Command retention-probe is a THROWAWAY diagnostic for the production
// retention failure (handoff: /tmp/bqckup-go-retention-bug-handoff.md).
// It opens the production repository exactly like bqckup does and prints
// UNREDACTED step errors. Excluded from normal builds by the bqckupprobe
// tag (build with: go build -tags bqckupprobe -o /tmp/retention-probe ./cmd/retention-probe).
// Never commit or ship this file.
//
// It never prints passwords, access keys, secret keys, or the endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	backuprestic "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/bqckup/bqckup-go/internal/config"
	erestic "github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
)

func main() {
	args := os.Args[1:]
	doPrune := false
	if len(args) > 0 && args[0] == "-prune" {
		doPrune = true
		args = args[1:]
	}
	if len(args) < 1 {
		fatal(nil, "usage: retention-probe [-prune] <site-name> [config-dir]")
	}
	siteName := args[0]
	configDir := "/etc/bqckup"
	if len(args) > 1 {
		configDir = args[1]
	}
	ctx := context.Background()

	cfg, err := config.Load(ctx, configDir)
	fatal(err, "load config "+configDir)

	var site *config.Site
	for i := range cfg.Sites {
		if cfg.Sites[i].Name == siteName {
			site = &cfg.Sites[i]
			break
		}
	}
	if site == nil {
		fatal(nil, fmt.Sprintf("site %q not found in config", siteName))
	}
	if len(site.Destinations) == 0 {
		fatal(nil, fmt.Sprintf("site %q has no destinations", siteName))
	}
	storageName := site.Destinations[0].Storage
	storageConfig, ok := cfg.Storages[storageName]
	if !ok {
		fatal(nil, fmt.Sprintf("storage %q not found in config", storageName))
	}

	repoURL, err := backuprestic.RepositoryURL(storageConfig, site.Name)
	fatal(err, "build repository URL")
	password, ok := os.LookupEnv(site.Incremental.PasswordEnv)
	if !ok || password == "" {
		fatal(nil, fmt.Sprintf("environment variable %q not set or empty", site.Incremental.PasswordEnv))
	}
	rc := backuprestic.RepoConfig{
		URL:             repoURL,
		Password:        password,
		AccessKeyID:     storageConfig.AccessKeyID,
		SecretAccessKey: storageConfig.SecretAccessKey,
		Region:          storageConfig.Region,
		Endpoint:        storageConfig.Endpoint,
		Bucket:          storageConfig.Bucket,
		Prefix:          path.Join(storageConfig.Prefix, "restic", site.Name),
	}

	fmt.Printf("site=%s mode=%s storage=%s type=%s bucket=%s prefix=%q keep_last=%d\n",
		site.Name, site.BackupMode, storageName, storageConfig.Type,
		rc.Bucket, rc.Prefix, site.Policy.KeepLast)
	fmt.Println("warning: no lock is taken; make sure no backup is running")

	var b backend.Backend
	if strings.HasPrefix(rc.URL, "s3:") {
		b, err = backend.NewS3(ctx, backend.S3Options{
			Bucket:          rc.Bucket,
			Endpoint:        rc.Endpoint,
			Prefix:          path.Join(storageConfig.Prefix, "restic", site.Name),
			Region:          rc.Region,
			AccessKeyID:     rc.AccessKeyID,
			SecretAccessKey: rc.SecretAccessKey,
		})
	} else {
		b = backend.NewLocal(rc.URL)
	}
	fatal(err, "open backend")

	r, err := repository.Open(ctx, b, rc.Password)
	fatal(err, "open repository")

	// Inventory: one line of counts per object type (what exists in the repo).
	var counts []string
	for _, t := range []erestic.FileType{erestic.ConfigFile, erestic.KeyFileType, erestic.IndexFile, erestic.SnapshotFile, erestic.LockFile, erestic.DataFile} {
		count := 0
		err := b.List(ctx, t, func(h erestic.Handle, size int64) error {
			count++
			return nil
		})
		if err != nil {
			fatal(err, "inventory "+fileTypeName(t))
		}
		counts = append(counts, fmt.Sprintf("%s=%d", fileTypeName(t), count))
	}
	fmt.Printf("inventory: %s\n", strings.Join(counts, " "))

	if !doPrune {
		fmt.Println("inventory OK; rerun with -prune to exercise ForgetAndPrune")
		return
	}

	// The exact production operation, unredacted.
	res, err := r.ForgetAndPrune(ctx, site.Policy.KeepLast, "site:"+site.Name)
	if err != nil {
		fmt.Println("ForgetAndPrune FAILED:")
		chain(err)
		os.Exit(1)
	}
	fmt.Printf("prune OK: snapshots_removed=%d packs_removed=%d bytes_reclaimed=%d\n",
		res.SnapshotsRemoved, res.PacksRemoved, res.BytesReclaimed)
}

func fatal(err error, step string) {
	if err == nil {
		return
	}
	fmt.Printf("%s FAILED:\n", step)
	chain(err)
	os.Exit(1)
}

func chain(err error) { walk(err, 0) }

func fileTypeName(t erestic.FileType) string {
	switch t {
	case erestic.ConfigFile:
		return "config"
	case erestic.KeyFileType:
		return "keys"
	case erestic.IndexFile:
		return "index"
	case erestic.SnapshotFile:
		return "snapshots"
	case erestic.LockFile:
		return "locks"
	case erestic.DataFile:
		return "data"
	default:
		return fmt.Sprintf("type-%v", t)
	}
}

func walk(err error, depth int) {
	if err == nil {
		return
	}
	fmt.Printf("%s- %v\n", strings.Repeat("    ", depth), err)
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range multi.Unwrap() {
			walk(e, depth+1)
		}
		return
	}
	walk(errors.Unwrap(err), depth+1)
}
