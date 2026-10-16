#!/usr/bin/env sh
set -eu

# The archived dl.min.io binaries now return 410. Extract the same releases
# from official, digest-pinned linux/amd64 OCI images without starting them.
target=${1:-}
case "$target" in
  /tmp/coconet-phase7-artifact.*|/tmp/coconet-phase7-self-hosted.*) ;;
  *) printf 'MinIO fixture requires an owned test directory.\n' >&2; exit 1 ;;
esac
test -d "$target"
command -v docker >/dev/null
container=""
owned_image=""
cleanup() {
  if test -n "$container"; then docker rm "$container" >/dev/null 2>&1 || true; fi
  if test -n "$owned_image"; then docker image rm "$owned_image" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

extract() {
  image=$1
  binary=$2
  destination=$3
  test ! -e "$target/$destination"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    owned_image=$image
    docker pull --platform linux/amd64 "$image" >/dev/null
  fi
  container=$(docker create --platform linux/amd64 "$image")
  docker cp "$container:/usr/bin/$binary" "$target/$destination"
  chmod 0700 "$target/$destination"
  docker rm "$container" >/dev/null
  container=""
  if test -n "$owned_image"; then
    docker image rm "$owned_image" >/dev/null
    owned_image=""
  fi
}

extract quay.io/minio/minio@sha256:a1a8bd4ac40ad7881a245bab97323e18f971e4d4cba2c2007ec1bedd21cbaba2 minio minio.RELEASE.2025-09-07T16-13-09Z
extract quay.io/minio/mc@sha256:eb4ea9884b77704230e2423e9004d2fa738dc272876b9cc41a297d29443b8780 mc mc.RELEASE.2025-08-13T08-35-41Z
