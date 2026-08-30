package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bqckup/bqckup-go/internal/app"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/spf13/cobra"
)

const storageTimeLayout = "02 Jan 2006 15:04"

type packageJSON struct {
	Destination string    `json:"destination"`
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
}

type snapshotJSON struct {
	ID        string    `json:"id"`
	Paths     []string  `json:"paths"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

type incrementalListingJSON struct {
	Snapshots        []snapshotJSON `json:"snapshots"`
	DatabasePackages []packageJSON  `json:"database_packages"`
}

func newStorageCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "storage", Short: "Inspect live remote storage contents"}
	var site string
	list := &cobra.Command{
		Use:     "list <destination> --site <site>",
		Short:   "List the live contents of one remote destination for one site",
		Example: "  bqckup storage list testing --site incremental-test",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "storage list requires exactly one destination")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if site == "" {
				return usageError(cmd, "--site is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				listing, err := application.ListRemoteContents(cmd.Context(), site, args[0])
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeStorageJSON(cmd, listing)
				}
				return writeStorageText(cmd.OutOrStdout(), listing)
			})
		},
	}
	list.Flags().StringVar(&site, "site", "", "site whose contents to list (required)")
	command.AddCommand(list)

	var key, expiry string
	var expiryDuration time.Duration
	link := &cobra.Command{
		Use:     "link <destination> --key <object-key>",
		Short:   "Create a temporary download link for one remote package",
		Example: "  bqckup storage link testing --key bqckup/server/site/timestamp/files.tar.gz",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "storage link requires exactly one destination; pass the object key with --key")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if key == "" {
				return usageError(cmd, "--key is required")
			}
			hours, err := parseExpiryHours(expiry)
			if err != nil {
				return usageError(cmd, "--expires must be a whole number of hours between 1 and 24, like 6h")
			}
			expiryDuration = time.Duration(hours) * time.Hour
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				link, err := application.Link(cmd.Context(), args[0], key, expiryDuration)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeLinkJSON(cmd, args[0], link)
				}
				return writeLinkText(cmd.OutOrStdout(), cmd.ErrOrStderr(), link)
			})
		},
	}
	link.Flags().StringVar(&key, "key", "", "object key to link, as printed by storage list (required)")
	link.Flags().StringVar(&expiry, "expires", "24h", "link validity in whole hours, 1-24")
	command.AddCommand(link)
	return command
}

// parseExpiryHours accepts only whole-hour values between 1 and 24.
func parseExpiryHours(value string) (int, error) {
	if !strings.HasSuffix(value, "h") {
		return 0, fmt.Errorf("invalid expiry")
	}
	hours, err := strconv.Atoi(strings.TrimSuffix(value, "h"))
	if err != nil || hours < 1 || hours > 24 {
		return 0, fmt.Errorf("invalid expiry")
	}
	return hours, nil
}

func writeLinkText(stdout, stderr io.Writer, link storage.DownloadLink) error {
	if _, err := fmt.Fprintln(stdout, link.URL); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stderr, "Link expires at %s.\n", link.ExpiresAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stderr, "Anyone with this link can download the file.")
	return err
}

type linkJSON struct {
	URL         string    `json:"url"`
	Key         string    `json:"key"`
	Destination string    `json:"destination"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func writeLinkJSON(cmd *cobra.Command, destination string, link storage.DownloadLink) error {
	return encodeLinkJSON(json.NewEncoder(cmd.OutOrStdout()), destination, link)
}

func encodeLinkJSON(encoder *json.Encoder, destination string, link storage.DownloadLink) error {
	encoder.SetEscapeHTML(false)
	return encoder.Encode(linkJSON{
		URL:         link.URL,
		Key:         link.Key,
		Destination: destination,
		ExpiresAt:   link.ExpiresAt.UTC(),
	})
}

func writeStorageText(output io.Writer, listing backup.Listing) error {
	if listing.Mode == "incremental" {
		return writeSnapshotText(output, listing)
	}
	return writePackageText(output, listing)
}

func writePackageText(output io.Writer, listing backup.Listing) error {
	if len(listing.Packages) == 0 {
		_, err := fmt.Fprintf(output, "No packages found for site %q on %q.\n", listing.Site, listing.Destination)
		return err
	}
	databasePackages := make([]backup.PackageRow, 0)
	filePackages := make([]backup.PackageRow, 0)
	for _, pkg := range listing.Packages {
		if isDatabasePackage(pkg.Key) {
			databasePackages = append(databasePackages, pkg)
		} else {
			filePackages = append(filePackages, pkg)
		}
	}
	if len(databasePackages) == 0 {
		return writePackageRows(output, filePackages)
	}
	if len(filePackages) > 0 {
		if _, err := fmt.Fprintln(output, "FILE PACKAGES"); err != nil {
			return err
		}
		if err := writePackageRows(output, filePackages); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(output); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(output, "DATABASE PACKAGES"); err != nil {
		return err
	}
	return writePackageRows(output, databasePackages)
}

func isDatabasePackage(key string) bool {
	return strings.Contains(key, "/databases/")
}

func writeSnapshotText(output io.Writer, listing backup.Listing) error {
	if len(listing.Snapshots) == 0 && len(listing.DatabasePackages) == 0 {
		_, err := fmt.Fprintf(output, "No snapshots found for site %q on %q.\n", listing.Site, listing.Destination)
		return err
	}
	if len(listing.Snapshots) > 0 {
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "ID\tPATHS\tSIZE\tCREATED AT"); err != nil {
			return err
		}
		for _, snapshot := range listing.Snapshots {
			size := "-"
			if snapshot.Size > 0 {
				size = humanBytes(snapshot.Size)
			}
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n",
				snapshot.ID, strings.Join(snapshot.Paths, ", "), size, snapshot.CreatedAt.UTC().Format(storageTimeLayout)); err != nil {
				return err
			}
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	if len(listing.DatabasePackages) > 0 {
		if len(listing.Snapshots) > 0 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(output, "DATABASE PACKAGES"); err != nil {
			return err
		}
		if err := writePackageRows(output, listing.DatabasePackages); err != nil {
			return err
		}
	}
	return nil
}

func writePackageRows(output io.Writer, packages []backup.PackageRow) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "DESTINATION\tKEY\tSIZE\tCREATED AT"); err != nil {
		return err
	}
	for _, pkg := range packages {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n",
			pkg.Destination, pkg.Key, humanBytes(pkg.Size), pkg.CreatedAt.UTC().Format(storageTimeLayout)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeStorageJSON(cmd *cobra.Command, listing backup.Listing) error {
	return encodeStorageJSON(json.NewEncoder(cmd.OutOrStdout()), listing)
}

func encodeStorageJSON(encoder *json.Encoder, listing backup.Listing) error {
	encoder.SetEscapeHTML(false)
	if listing.Mode == "incremental" {
		rows := make([]snapshotJSON, 0, len(listing.Snapshots))
		for _, snapshot := range listing.Snapshots {
			rows = append(rows, snapshotJSON{ID: snapshot.ID, Paths: snapshot.Paths, Size: snapshot.Size, CreatedAt: snapshot.CreatedAt.UTC()})
		}
		if len(listing.DatabasePackages) == 0 {
			return encoder.Encode(rows)
		}
		packages := make([]packageJSON, 0, len(listing.DatabasePackages))
		for _, pkg := range listing.DatabasePackages {
			packages = append(packages, packageJSON{
				Destination: pkg.Destination, Key: pkg.Key, Size: pkg.Size, CreatedAt: pkg.CreatedAt.UTC(),
			})
		}
		return encoder.Encode(incrementalListingJSON{Snapshots: rows, DatabasePackages: packages})
	}
	rows := make([]packageJSON, 0, len(listing.Packages))
	for _, pkg := range listing.Packages {
		rows = append(rows, packageJSON{
			Destination: pkg.Destination, Key: pkg.Key, Size: pkg.Size, CreatedAt: pkg.CreatedAt.UTC(),
		})
	}
	return encoder.Encode(rows)
}
