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
a clean path outside `/.odraz`, `target.key` is exactly the item's own object, and the thumbnail name is its own.

## Archive

1. The file is streamed to the target once, while its SHA-256 and the target's check are computed.
2. The copy is verified: by the target's checksum (no download), or by reading it back if that cannot decide or if
   `VerifyDownload` is set. The record copy is verified the same way.
3. The thumbnail and the drive record are written, the record last.
4. The file is looked at again. Changed: everything is undone and the file stays. Gone: the archive is kept.
   Unchanged: it is deleted.

Running `Archive` again after a failure finishes the job.

## Restore, delete, rescan

- **Restore** never overwrites (`name (restored).ext`, then `(restored 2)` ...), reads the restored file back, and only
  then removes the copy in the target (unless `KeepCopy`).
- **Delete** removes every version of the original in the target, then the records and thumbnail.
- **Rescan** recreates missing drive records from the target's copies, only for items whose original is complete in the
  target and absent from the drive. Everything else is reported as an orphan. No original is downloaded.

## Limits

- One operation per file at a time is the caller's job.
- A change to the file between the last look and the delete is not detected (servers offer no conditional delete).
- The servers' trash bins keep deleted files: space is freed when they are emptied (see `testenv/README.md`).
- Videos get a placeholder thumbnail unless the application supplies a frame; so do pictures Go cannot decode
  (HEIC, AVIF) when the server has no preview.
- If a damaged copy is found while finishing an earlier run, the file is archived again and the old record stays.
- Memory per item: one upload part (16 MiB for most files), plus at most 20 MiB while a thumbnail is made from the
  file itself, plus the decoded picture (pictures over 100 megapixels are not decoded).
