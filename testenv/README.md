# Test servers

```sh
eval "$(testenv/seed.sh)"              # OpenCloud, Nextcloud and MinIO, fresh (or: seed.sh minio)
go test -tags integration -race -count=1 -v ./
docker compose -f testenv/docker-compose.yml down -v
```

Throwaway containers on ports 8182, 9400 and 9410. The test writes in a folder of its own and removes it. To use
another object store, set the `ODRAZ_TEST_S3_*` variables for a bucket made for testing.

## Observed (2026-11-26, the images' current releases)

| | OpenCloud | Nextcloud |
|---|---|---|
| Server preview of a photo (JPEG, non-ASCII name) | yes (JPEG) | yes (JPEG) |
| Server preview of an `.mp4` | no | no (stock image) |
| A deleted file | goes to the trash bin | goes to the trash bin |
| Quota "used" right after deleting a 30 MB file | back to the earlier value | unchanged (Nextcloud caches it, so not conclusive) |
| Disk space right after deleting | **still used** (the file is listed in the trash) | **still used** (the file is in `files_trashbin`) |

So **archiving does not free disk space until the server's trash bin is emptied** (by hand or by its retention
policy). The library does not empty trash bins: the SDK has no call for it.
