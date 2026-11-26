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
