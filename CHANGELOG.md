# Changelog

## [Unreleased]

### Added

- Archive photos and videos from a drive to an object store: verified before the original is removed.
- `DriveKey`; operations on one file take turns.
- List, thumbnails, restore (never overwrites), permanent delete, rescan, and the target in the drive manifest.
- The original is deleted only if unchanged on drives that can promise it (Nextcloud); a rescan overwrites nothing
  and does not record a file twice.
