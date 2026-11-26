#!/bin/sh
# Starts the test servers (state on tmpfs) and prints the variables of the integration test.
#   eval "$(testenv/seed.sh)"          # all three
#   eval "$(testenv/seed.sh minio)"    # only the object store (what CI runs)
set -e
cd "$(dirname "$0")"
services=${*:-"minio opencloud nextcloud"}
docker compose up -d --force-recreate $services >&2
wait_for() { # description, command...
  what=$1; shift; i=0
  until "$@" >/dev/null 2>&1; do
    i=$((i+1)); [ $i -gt 120 ] && { echo "$what did not come up" >&2; docker compose logs --tail 30 >&2; exit 1; }
    sleep 3
  done
}
for s in $services; do case $s in
minio)
  go run ./mkbucket localhost:9410 odraz-test-only odraz-test-only-not-a-secret archive-target >&2
  go run ./mkbucket localhost:9410 odraz-test-only odraz-test-only-not-a-secret archive-source >&2
  echo "export ODRAZ_TEST_S3_ENDPOINT=http://localhost:9410"
  echo "export ODRAZ_TEST_S3_BUCKET=archive-target"
  echo "export ODRAZ_TEST_S3_SOURCE_BUCKET=archive-source"
  echo "export ODRAZ_TEST_S3_KEY=odraz-test-only"
  echo "export ODRAZ_TEST_S3_SECRET=odraz-test-only-not-a-secret" ;;
opencloud)
  wait_for opencloud curl -skf -m 5 -u admin:admin https://localhost:9400/graph/v1.0/me
  token=$(docker compose exec -T opencloud opencloud auth-app create --user-name=admin --expiration=24h 2>&1 | sed -n 's/^ *token: *//p')
  [ -n "$token" ] || { echo "no app token" >&2; exit 1; }
  echo "export ODRAZ_TEST_OPENCLOUD_URL=https://localhost:9400"
  echo "export ODRAZ_TEST_OPENCLOUD_USER=admin"
  echo "export ODRAZ_TEST_OPENCLOUD_TOKEN='$token'" ;;
nextcloud)
  wait_for nextcloud sh -c 'curl -sf -m 5 http://localhost:8182/status.php | grep -q "\"installed\":true"'
  echo "export ODRAZ_TEST_NEXTCLOUD_URL=http://localhost:8182"
  echo "export ODRAZ_TEST_NEXTCLOUD_USER=admin"
  echo "export ODRAZ_TEST_NEXTCLOUD_PASSWORD=admin-pass-123" ;;
esac; done
