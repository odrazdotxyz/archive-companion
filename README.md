# archive-companion

A Go library that archives photos and videos from a self-hosted cloud drive to an object store (S3), keeping a
thumbnail and a record on the drive. Built on the [Odraz Go SDK](https://github.com/odrazdotxyz/odraz-go-sdk).
Apache-2.0. Not a service. **Work in progress.**

```
drive:   /.odraz/archive/photos/<ID>.json   the record (source of truth for the item)
         /.odraz/archive/photos/<ID>.jpg    the thumbnail
target:  /odraz/<drive-key>/<ID>/<file>     the original
         /odraz/<drive-key>/records/<ID>.json   a copy of the record, so the target explains itself
```

## Use

```go
a, err := archive.New(
    archive.Source{FS: drive, DriveKey: archive.DriveKey(serverURL, account, driveID)},
    archive.Target{FS: bucket, Descriptor: archive.TargetDescriptor{Endpoint: endpoint, Bucket: name, Prefix: prefix}},
    archive.Options{},                                    // VerifyDownload for the stronger check
)
rec, err := a.Archive(ctx, "/Pictures/IMG_1.jpg", archive.ArchiveInput{Thumbnail: nil})
items, problems, err := a.List(ctx)
res, err := a.Restore(ctx, rec.ID, archive.RestoreInput{})
err = a.Delete(ctx, rec.ID)                               // permanent
found, err := a.Rescan(ctx)
err = archive.SetTarget(ctx, drive, descriptor)           // remembered on the drive, without credentials
```

`drive` and `bucket` are `files.FS` values from the SDK; the target must implement `files.ObjectStore` (the S3
adapter does). Rules, formats and limits: [docs/format.md](docs/format.md). Test servers: [testenv/](testenv/README.md).

**Note:** servers move deleted files to a trash bin, so archiving frees disk space only when that is emptied.
