package archive

import "errors"

var (
	// ErrBadRecord: a record (or a drive key or id) that cannot be trusted.
	ErrBadRecord = errors.New("archive: invalid record")
	// ErrNotMedia: only photos and videos are archived.
	ErrNotMedia = errors.New("archive: not a photo or a video")
	// ErrSourceChanged: the file changed while it was being archived; it was kept.
	ErrSourceChanged = errors.New("archive: the file changed while it was being archived")
	// ErrNotVerified: a copy could not be shown to hold the file's content; the
	// file it was made from was kept.
	ErrNotVerified = errors.New("archive: the copy could not be verified")
	// ErrWrongTarget: the item is stored somewhere else than the connected target.
	ErrWrongTarget = errors.New("archive: the item is stored in another target")
)
