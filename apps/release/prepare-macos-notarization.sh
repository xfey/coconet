#!/usr/bin/env bash

set -euo pipefail

release_argument="${1:-}"
output_argument="${2:-}"

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

[[ -n "${release_argument}" && -n "${output_argument}" ]] || {
  printf 'Usage: prepare-macos-notarization.sh <public-release-directory> <output-directory>\n' >&2
  exit 2
}
[[ "$(uname -s)" == "Darwin" ]] || fail "macOS notarization archives must be prepared on macOS"
for command_name in codesign ditto node shasum tar; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "required notarization preparation command is unavailable: ${command_name}"
done

release_root="$(cd "${release_argument}" && pwd -P)"
[[ -f "${release_root}/release.json" && ! -L "${release_root}/release.json" ]] || fail "public release metadata is unavailable"

IFS=$'\t' read -r client_version developer_id_signed secure_timestamp < <(
  node --input-type=module - "${release_root}/release.json" <<'NODE'
import { readFileSync } from "node:fs";

const metadata = JSON.parse(readFileSync(process.argv[2], "utf8"));
if (
  metadata?.schema_version !== 2 || metadata.channel !== "stable" ||
  !/^\d+\.\d+\.\d+$/u.test(metadata.client_version || "") ||
  typeof metadata.macos?.developer_id_signed !== "boolean" ||
  typeof metadata.macos?.secure_timestamp !== "boolean"
) process.exit(1);
process.stdout.write(
  `${metadata.client_version}\t${metadata.macos.developer_id_signed}\t${metadata.macos.secure_timestamp}\n`,
);
NODE
)
[[ -n "${client_version}" ]] || fail "public release metadata is invalid"
[[ "${developer_id_signed}" == "true" ]] || fail "macOS archives are not Developer ID signed"
[[ "${secure_timestamp}" == "true" ]] || fail "macOS archives do not use a secure timestamp"

mkdir -p "$(dirname "${output_argument}")"
output_parent="$(cd "$(dirname "${output_argument}")" && pwd -P)"
output_directory="${output_parent}/$(basename "${output_argument}")"
[[ ! -e "${output_directory}" ]] || fail "notarization output already exists: ${output_directory}"

staging_directory="${output_parent}/.$(basename "${output_directory}").notary-preparation.$$"
[[ ! -e "${staging_directory}" ]] || fail "notarization staging path already exists"
mkdir "${staging_directory}"
cleanup() {
  case "${staging_directory}" in
    "${output_parent}"/.*.notary-preparation.*)
      if [[ -d "${staging_directory}" ]]; then
        find "${staging_directory}" -depth -delete
      fi
      ;;
  esac
}
trap cleanup EXIT

verify_signature() {
  local executable="$1"
  local expected_identifier="$2"
  local expected_authority="$3"
  local details

  codesign --verify --strict --verbose=2 "${executable}" >/dev/null 2>&1 || fail "invalid signature: ${expected_identifier}"
  details="$(codesign -dvvv "${executable}" 2>&1)"
  [[ "${details}" == *"Identifier=${expected_identifier}"* ]] || fail "unexpected signing identifier: ${expected_identifier}"
  [[ "${details}" == *"${expected_authority}"* ]] || fail "unexpected signing authority: ${expected_identifier}"
  [[ "${details}" == *"flags="*"runtime"* ]] || fail "hardened runtime is missing: ${expected_identifier}"
  [[ "${details}" == *"Timestamp="* ]] || fail "secure timestamp is missing: ${expected_identifier}"
}

for architecture in amd64 arm64; do
  bundle_name="coconet-${client_version}-darwin-${architecture}"
  archive_name="${bundle_name}.tar.gz"
  archive="${release_root}/${archive_name}"
  checksum="${archive}.sha256"
  [[ -f "${archive}" && ! -L "${archive}" ]] || fail "macOS archive is unavailable: ${archive_name}"
  [[ -f "${checksum}" && ! -L "${checksum}" ]] || fail "macOS checksum is unavailable: ${archive_name}"
  expected_hash="$(awk -v name="${archive_name}" '$2 == name { print $1 }' "${checksum}")"
  [[ "${expected_hash}" =~ ^[0-9a-f]{64}$ ]] || fail "macOS checksum is invalid: ${archive_name}"
  actual_hash="$(shasum -a 256 "${archive}" | awk '{ print $1 }')"
  [[ "${actual_hash}" == "${expected_hash}" ]] || fail "macOS archive hash mismatch: ${archive_name}"

  extraction_root="${staging_directory}/extract-${architecture}"
  mkdir "${extraction_root}"
  while IFS= read -r entry; do
    [[ "${entry}" == "${bundle_name}" || "${entry}" == "${bundle_name}/"* ]] || fail "unsafe macOS archive entry: ${archive_name}"
  done < <(tar -tzf "${archive}")
  tar -xzf "${archive}" -C "${extraction_root}"
  bundle_root="${extraction_root}/${bundle_name}"
  [[ -d "${bundle_root}" && ! -L "${bundle_root}" ]] || fail "macOS bundle root is invalid: ${archive_name}"

  verify_signature "${bundle_root}/bin/coconet" "work.coconet.runtime" "Authority=Developer ID Application:"
  verify_signature "${bundle_root}/bin/coconet-bootstrap" "work.coconet.bootstrap" "Authority=Developer ID Application:"
  verify_signature "${bundle_root}/bin/lib/node/node" "node" "Authority=Developer ID Application: Node.js Foundation (HX7739G8FX)"

  submission="${staging_directory}/${bundle_name}-notary.zip"
  ditto --norsrc -c -k --keepParent "${bundle_root}" "${submission}"
  submission_hash="$(shasum -a 256 "${submission}" | awk '{ print $1 }')"
  printf '%s  %s\n' "${submission_hash}" "$(basename "${submission}")" >"${submission}.sha256"
  find "${extraction_root}" -depth -delete
done

mv "${staging_directory}" "${output_directory}"
trap - EXIT
printf 'macOS notarization submission archives created at %s\n' "${output_directory}"
printf 'No archive was submitted to Apple.\n'
