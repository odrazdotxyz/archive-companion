# Formats and safety rules

## Layout

```
drive:   /.odraz/archive/photos/<ID>.json        record: the source of truth for the item
         /.odraz/archive/photos/<ID>.jpg         thumbnail, JPEG, long edge at most 512 px
         /.odraz/manifest.json                   sections.archive: {endpoint, bucket, prefix}
target:  /odraz/<drive-key>/<ID>/<file name>     the original, unchanged
         /odraz/<drive-key>/records/<ID>.json    copy of the record
```

`<ID>` is a ULID. `<drive-key>` (a-z, 0-9, `-`, at most 64) is chosen by the application and must be the same on
every device for the same drive.

## Record

`version` (1), `id`, `target` {`endpoint`, `bucket`, `prefix`, `key`}, `sha256`, `size`, `mime`, `originalPath`,
`modTime`, `takenAt`, `width`, `height`, `thumb` {`file`, `w`, `h`, `placeholder`}, `archivedAt`, `rebuilt`.
Unknown top-level fields are kept. A record never holds credentials.

Records are **untrusted input**. A record is refused unless its id is a ULID matching its file name, `originalPath` is
a clean path outside `/.odraz` (in any case of letters), `target.key` is exactly the item's own object, and the thumbnail name is its own.

A target's `endpoint` with user info, a query or a fragment (`@`, `?`, `#`) could carry a credential. Such a
descriptor is refused when connecting and in the manifest, and a record naming one cannot be restored or deleted
(`ErrWrongTarget`, without repeating it). `List` still returns that record as it is on the drive.

## Archive

1. The file is streamed to the target once, while its SHA-256 and the target's check are computed.
2. The copy is verified: by the target's checksum (no download), or by reading it back if that cannot decide or if
   `VerifyDownload` is set. The record copy is verified the same way.
3. The thumbnail and the drive record are written, the record last.
4. The file is looked at again. Changed: everything is undone and the file stays. Gone: the archive is kept.
   Unchanged: it is deleted, on condition that it is still unchanged where the drive can promise that (see Limits).
   A refused delete counts as changed. When a later run is finishing an earlier one, a changed file stays and so
   does the earlier record, whose verified copy holds the content as it was; the next run archives the file anew.

Running `Archive` again after a failure finishes the job.

## Restore, delete, rescan

- **Restore** never overwrites (`name (restored).ext`, then `(restored 2)` ...), reads the restored file back, and only
  then removes the copy in the target (unless `KeepCopy`).
- **Delete** removes every version of the original in the target, then the records and thumbnail.
- **Rescan** recreates missing drive records from the target's copies, only for items whose original is complete in the
  target and absent from the drive. A copy of a file that a drive record already has (same checksum, size and
  original path) is not recorded a second time: it is reported as "already archived as <ID>"; of several copies
  without a record the newest is taken. Everything else is reported as an orphan. No original is downloaded. Nothing on
  the drive is overwritten: a thumbnail left there is kept, and a record that cannot be used is reported as a
  problem and left alone.

## Limits

- Archive, Restore, Delete and Rescan of one file take turns within a process. Across processes or devices the application
  must not run them on the same file at once; the library re-checks the record and the file just before removing
  anything, which narrows but cannot close that window.
- A **drive key must belong to one drive only** (use `DriveKey(server, account, drive)`): drives sharing a key and a
  target would be offered, and could permanently delete, each other's files.
- After a restore whose write failed, whatever is at that path is left alone; the next try uses a "(restored)" name.
- A change to the file between the last look and the delete is caught only by a drive that deletes conditionally
  (`files.ConditionalDeleter`, `If-Match`): of the SDK's drives, **Nextcloud**. On every other drive, among them
  **OpenCloud** (it ignores `If-Match` on DELETE), generic WebDAV and S3, the window stays open: such a change is
  not detected and the changed file is deleted.
- The servers' trash bins keep deleted files: space is freed when they are emptied (see `testenv/README.md`).
- Videos get a placeholder thumbnail unless the application supplies a frame; so do pictures Go cannot decode
  (HEIC, AVIF) when the server has no preview.
- Rescan reads the drive records when it starts: a file archived while it runs can still get a second record.
- If a damaged copy is found while finishing an earlier run, the file is archived again and the old record stays.
- Memory per item: one upload part (16 MiB for most files), plus at most 20 MiB while a thumbnail is made from the
  file itself, plus the decoded picture, at most about 200 MiB (larger ones, such as progressive JPEGs over about 13 megapixels,
  get a placeholder unless the server or the application supplies an image).
