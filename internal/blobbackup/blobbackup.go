// Package blobbackup is the production implementation of store.Backup: a thin
// adapter that snapshots the SQLite database to a single Azure Blob and restores
// it on a cold boot (ADR-0003). It is the one place the Azure SDK enters the
// binary — everything else, including every test, works against the in-process
// fakes that exercise the same store.Backup interface — so keeping the SDK
// confined here is what lets local QA and CI stay zero-config, offline, and free
// of any Azure credential or network call.
package blobbackup

import (
	"context"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/emepetres/life-ledger/internal/store"
)

// Compile-time guarantee that Sink conforms to the seam it plugs into. This
// adapter is deliberately not unit-tested against real Azure (it is a thin
// pass-through to the SDK), so this assertion is the standing check that its
// method set still matches store.Backup as that interface evolves.
var _ store.Backup = (*Sink)(nil)

// blobName is the fixed name of the single snapshot blob within the container.
// The container holds exactly one database snapshot, overwritten on every write,
// so the name is a constant rather than configuration: the container URL alone
// (LIFELEDGER_BACKUP_BLOB_URL) locates the backup.
const blobName = "expenses.db"

// Sink stores database snapshots as a single blob in an Azure Storage container,
// authenticating with a managed identity (no account key). It satisfies
// store.Backup.
type Sink struct {
	container *container.Client
}

// New builds a Sink targeting the given container URL, authenticating via
// DefaultAzureCredential — in production that resolves to the Container App's
// managed identity, so no account key or connection string is ever handled.
//
// Construction is offline: neither the credential nor the client makes a network
// call here. The first real request happens on Load (restore-on-boot) or Save
// (backup-on-write), so an unreachable or misconfigured Blob surfaces there — on
// boot, as a fatal restore error rather than a store that silently starts empty.
func New(containerURL string) (*Sink, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("building Azure credential: %w", err)
	}
	c, err := container.NewClient(containerURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("building blob container client for %q: %w", containerURL, err)
	}
	return &Sink{container: c}, nil
}

// Save uploads the snapshot at snapshotPath as the container's single blob,
// overwriting any previous snapshot. The store hands us a consistent,
// sidecar-free file (produced by VACUUM INTO), so a plain block-blob upload is a
// faithful durable copy.
func (s *Sink) Save(ctx context.Context, snapshotPath string) error {
	f, err := os.Open(snapshotPath)
	if err != nil {
		return fmt.Errorf("opening snapshot %q: %w", snapshotPath, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := s.container.NewBlockBlobClient(blobName).UploadFile(ctx, f, nil); err != nil {
		return fmt.Errorf("uploading snapshot to blob %q: %w", blobName, err)
	}
	return nil
}

// Load writes the latest snapshot blob to destPath, reporting whether one
// existed. A missing blob — the legitimate first-ever boot — is reported as
// (false, nil) so the store starts fresh; any other failure (an unreachable or
// unauthorised container) returns a non-nil error so the boot aborts rather than
// risk starting empty over a good backup.
func (s *Sink) Load(ctx context.Context, destPath string) (bool, error) {
	f, err := os.Create(destPath)
	if err != nil {
		return false, fmt.Errorf("creating restore target %q: %w", destPath, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := s.container.NewBlobClient(blobName).DownloadFile(ctx, f, nil); err != nil {
		// Only a missing blob in an existing container is the legitimate first-ever
		// boot → (false, nil), start fresh. A missing *container* is misconfiguration
		// (a typo'd URL, an unprovisioned container, an identity on the wrong
		// account); treating it as "no backup" would start empty and overwrite the
		// real backup's location — exactly the silent-empty-start the seam forbids —
		// so it stays an error and aborts the boot.
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			// Remove the empty file we just created so the store sees an absent path.
			_ = f.Close()
			_ = os.Remove(destPath)
			return false, nil
		}
		return false, fmt.Errorf("downloading snapshot from blob %q: %w", blobName, err)
	}
	return true, nil
}
