package archive

import (
	"context"
	"errors"
	"fmt"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
)

// Delete removes an archived item for good: every version of its original in the
// target, then its records and thumbnail. After a failure it can be run again.
func (a *Archive) Delete(ctx context.Context, id string) error {
	if err := a.writable(); err != nil {
		return err
	}
	rec, unlock, err := a.lockItem(ctx, id)
	if errors.Is(err, odraz.ErrNotFound) {
		// Already removed; at most the thumbnail is left over.
		_ = a.src.FS.Delete(ctx, ThumbPath(id), false)
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	if err := a.purgeCopy(ctx, rec); err != nil {
		return fmt.Errorf("archive: delete %s: %w", id, err)
	}
	return a.removeDriveRecord(ctx, id)
}

// purgeCopy removes everything the target holds of an item. The record there
// goes last, so a half-removed item is still known to it.
func (a *Archive) purgeCopy(ctx context.Context, rec Record) error {
	if err := a.store.Purge(ctx, rec.Target.Key); err != nil {
		return err
	}
	// A folder someone put something else into stays.
	if err := a.store.Purge(ctx, itemFolder(a.src.DriveKey, rec.ID)); err != nil && !errors.Is(err, odraz.ErrNotEmpty) {
		return err
	}
	return a.store.Purge(ctx, BucketRecordPath(a.src.DriveKey, rec.ID))
}
