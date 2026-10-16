#!/usr/bin/env bash

set -euo pipefail

release_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${release_dir}/../.." && pwd)"
versions_file="${release_dir}/internal-versions.json"
output_argument="${1:-}"

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

for command_name in git node; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "required command is unavailable: ${command_name}"
done

cd "${repository_root}"
[[ -f "${versions_file}" && ! -L "${versions_file}" ]] || fail "internal version policy is unavailable"
[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || fail "internal candidates require a clean Git worktree"

source_revision="$(git rev-parse --verify HEAD)"
short_revision="$(git rev-parse --short=9 HEAD)"
IFS=$'\t' read -r client_version server_version < <(
  node --input-type=module - "${versions_file}" <<'NODE'
import { readFileSync } from "node:fs";

const path = process.argv[2];
const policy = JSON.parse(readFileSync(path, "utf8"));
const internalVersion = /^\d+\.\d+\.\d+-internal\.[1-9]\d*$/u;
if (
  policy?.schema_version !== 1 ||
  policy.channel !== "internal" ||
  !internalVersion.test(policy.client_version) ||
  !internalVersion.test(policy.server_version)
) {
  process.stderr.write("internal version policy is invalid\n");
  process.exit(1);
}
process.stdout.write(`${policy.client_version}\t${policy.server_version}\n`);
NODE
)
[[ -n "${client_version}" && -n "${server_version}" ]] || fail "internal versions could not be read"

if [[ -z "${output_argument}" ]]; then
  output_argument="bin/phase-7/internal-release-${short_revision}"
fi
mkdir -p "$(dirname "${output_argument}")"
output_parent="$(cd "$(dirname "${output_argument}")" && pwd -P)"
output_directory="${output_parent}/$(basename "${output_argument}")"
[[ ! -e "${output_directory}" ]] || fail "candidate output already exists: ${output_directory}"

staging_directory="${output_parent}/.$(basename "${output_directory}").candidate.$$"
[[ ! -e "${staging_directory}" ]] || fail "candidate staging path already exists"
cleanup() {
  case "${staging_directory}" in
    "${output_parent}"/.*.candidate.*)
      if [[ -d "${staging_directory}" ]]; then
        find "${staging_directory}" -depth -delete
      fi
      ;;
  esac
}
trap cleanup EXIT

"${release_dir}/build-release-artifacts.sh" \
  "${client_version}" "${server_version}" "${staging_directory}"

[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || fail "candidate build changed the tracked worktree"
if command -v sha256sum >/dev/null 2>&1; then
  sums_sha256="$(sha256sum "${staging_directory}/SHA256SUMS" | awk '{ print $1 }')"
else
  sums_sha256="$(shasum -a 256 "${staging_directory}/SHA256SUMS" | awk '{ print $1 }')"
fi

node --input-type=module - \
  "${staging_directory}/candidate.json" \
  "${source_revision}" "${client_version}" "${server_version}" "${sums_sha256}" <<'NODE'
import { writeFileSync } from "node:fs";

const [path, sourceRevision, clientVersion, serverVersion, sumsSHA256] = process.argv.slice(2);
const candidate = {
  schema_version: 1,
  channel: "internal",
  source_revision: sourceRevision,
  client_version: clientVersion,
  server_version: serverVersion,
  sha256sums_sha256: sumsSHA256,
};
writeFileSync(path, `${JSON.stringify(candidate, null, 2)}\n`, { mode: 0o644, flag: "wx" });
NODE

mv "${staging_directory}" "${output_directory}"
trap - EXIT
printf 'Coconet internal candidate created at %s\n' "${output_directory}"
printf 'source=%s client=%s server=%s\n' "${source_revision}" "${client_version}" "${server_version}"
