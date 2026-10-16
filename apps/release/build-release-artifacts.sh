#!/usr/bin/env bash

set -euo pipefail

release_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${release_dir}/../.." && pwd)"
client_version="${1:-}"
server_version="${2:-}"
output_argument="${3:-}"
node_version="24.22.0"
npm_package_json="installers/npm/package.json"
codex_plugin_root="plugins/codex/coconet"
claude_plugin_root="plugins/claude/coconet"
cursor_plugin_root="plugins/cursor/coconet"
sign_macos="false"
codesign_timestamp="secure"

shift $(( $# < 3 ? $# : 3 ))
while (( $# > 0 )); do
  case "$1" in
    --npm-package-json)
      [[ $# -ge 2 ]] || { printf 'Missing value for --npm-package-json.\n' >&2; exit 2; }
      npm_package_json="$2"
      shift 2
      ;;
    --codex-plugin-root)
      [[ $# -ge 2 ]] || { printf 'Missing value for --codex-plugin-root.\n' >&2; exit 2; }
      codex_plugin_root="$2"
      shift 2
      ;;
    --cursor-plugin-root)
      [[ $# -ge 2 ]] || { printf 'Missing value for --cursor-plugin-root.\n' >&2; exit 2; }
      cursor_plugin_root="$2"
      shift 2
      ;;
    --claude-plugin-root)
      [[ $# -ge 2 ]] || { printf 'Missing value for --claude-plugin-root.\n' >&2; exit 2; }
      claude_plugin_root="$2"
      shift 2
      ;;
    --sign-macos)
      sign_macos="true"
      shift
      ;;
    --codesign-timestamp=secure)
      codesign_timestamp="secure"
      shift
      ;;
    --codesign-timestamp=none)
      codesign_timestamp="none"
      shift
      ;;
    *)
      printf 'Unknown release option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [[ ! "${client_version}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] ||
  [[ ! "${server_version}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] ||
  [[ -z "${output_argument}" ]]; then
  printf 'Usage: build-release-artifacts.sh <client-version> <server-version> <output-directory> [options]\n' >&2
  exit 2
fi

for command_name in cmp curl go node npm python3 tar; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    printf 'Required release command is unavailable: %s\n' "${command_name}" >&2
    exit 1
  fi
done
if [[ "${sign_macos}" == "true" ]]; then
  [[ "$(uname -s)" == "Darwin" ]] || {
    printf 'Developer ID signing requires a macOS release host.\n' >&2
    exit 1
  }
  for command_name in codesign security; do
    command -v "${command_name}" >/dev/null 2>&1 || {
      printf 'Required macOS signing command is unavailable: %s\n' "${command_name}" >&2
      exit 1
    }
  done
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256_command=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  sha256_command=(shasum -a 256)
else
  printf 'A local SHA-256 command is required.\n' >&2
  exit 1
fi

cd "${repository_root}"
mkdir -p "$(dirname "${output_argument}")"
output_parent="$(cd "$(dirname "${output_argument}")" && pwd -P)"
output_directory="${output_parent}/$(basename "${output_argument}")"
if [[ -e "${output_directory}" ]]; then
  printf 'Release output already exists: %s\n' "${output_directory}" >&2
  exit 1
fi

json_version() {
  node --input-type=module - "${1}" <<'NODE'
import { readFileSync } from "node:fs";
const path = process.argv[2];
const value = JSON.parse(readFileSync(path, "utf8")).version;
if (typeof value !== "string" || value.length === 0) process.exit(1);
process.stdout.write(value);
NODE
}

build_reproducible_go_binary() {
  local binary_version="${1}"
  local output_path="${2}"
  local package_path="${3}"
  local repeat_path="${output_path}.repeat"
  local build_metadata
  local linker_flags="-buildid= -s -w -X main.version=${binary_version}"

  go build -trimpath -buildvcs=false -ldflags "${linker_flags}" -o "${output_path}" "${package_path}"
  go build -trimpath -buildvcs=false -ldflags "${linker_flags}" -o "${repeat_path}" "${package_path}"
  if ! cmp -s "${output_path}" "${repeat_path}"; then
    printf 'Repeated Go builds differed for %s.\n' "${package_path}" >&2
    exit 1
  fi
  rm -f "${repeat_path}"

  build_metadata="$(go version -m "${output_path}")"
  if [[ "${build_metadata}" == *"vcs.revision"* ]] || [[ "${build_metadata}" == *"vcs.modified"* ]]; then
    printf 'Release binary unexpectedly contains workspace VCS state: %s.\n' "${package_path}" >&2
    exit 1
  fi
}

npm_version="$(json_version "${npm_package_json}")"
codex_plugin_version="$(json_version "${codex_plugin_root}/.codex-plugin/plugin.json")"
claude_plugin_version="$(json_version "${claude_plugin_root}/.claude-plugin/plugin.json")"
cursor_plugin_version="$(json_version "${cursor_plugin_root}/.cursor-plugin/plugin.json")"
for component_version in "${npm_version}" "${codex_plugin_version}" "${claude_plugin_version}" "${cursor_plugin_version}"; do
  if [[ "${component_version}" != "${client_version}" ]]; then
    printf 'Client, npm, and Plugin versions must all equal %s.\n' "${client_version}" >&2
    exit 1
  fi
done

codesign_identity="${COCONET_MACOS_CODESIGN_IDENTITY:-Developer ID Application}"
if [[ "${sign_macos}" == "true" && -z "${COCONET_MACOS_CODESIGN_IDENTITY:-}" ]]; then
  developer_id_count="$(security find-identity -v -p codesigning 2>/dev/null | awk '/Developer ID Application:/ { count++ } END { print count+0 }')"
  if [[ "${developer_id_count}" != "1" ]]; then
    printf 'Expected exactly one valid Developer ID Application identity, found %s. Set COCONET_MACOS_CODESIGN_IDENTITY explicitly.\n' "${developer_id_count}" >&2
    exit 1
  fi
fi

verify_node_macos_signature() {
  local node_binary="$1"
  local signature_details

  codesign --verify --strict --verbose=2 "${node_binary}" >/dev/null 2>&1 || {
    printf 'Bundled macOS Node has an invalid upstream signature.\n' >&2
    exit 1
  }
  signature_details="$(codesign -dvv "${node_binary}" 2>&1)"
  if [[ "${signature_details}" != *"Authority=Developer ID Application: Node.js Foundation (HX7739G8FX)"* ]] ||
    [[ "${signature_details}" != *"TeamIdentifier=HX7739G8FX"* ]] ||
    [[ "${signature_details}" != *"flags="*"runtime"* ]]; then
    printf 'Bundled macOS Node does not match the expected Node.js Foundation hardened signature.\n' >&2
    exit 1
  fi
}

sign_macos_binary() {
  local binary_path="$1"
  local identifier="$2"
  local timestamp_argument=(--timestamp)
  if [[ "${codesign_timestamp}" == "none" ]]; then
    timestamp_argument=(--timestamp=none)
  fi

  codesign --force --sign "${codesign_identity}" --identifier "${identifier}" \
    --options runtime "${timestamp_argument[@]}" "${binary_path}"
  codesign --verify --strict --verbose=2 "${binary_path}" >/dev/null
  local signature_details
  signature_details="$(codesign -dvv "${binary_path}" 2>&1)"
  if [[ "${signature_details}" != *"Authority=Developer ID Application:"* ]] ||
    [[ "${signature_details}" != *"flags="*"runtime"* ]]; then
    printf 'Developer ID verification failed for %s.\n' "${identifier}" >&2
    exit 1
  fi
}

temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/coconet-release-artifacts.XXXXXX")"
cleanup() {
  case "${temporary_root}" in
    "${TMPDIR:-/tmp}"/coconet-release-artifacts.*)
      rm -rf "${temporary_root}"
      ;;
  esac
}
trap cleanup EXIT

input_root="${temporary_root}/inputs"
build_root="${temporary_root}/build"
bundle_root="${temporary_root}/bundles"
artifact_root="${temporary_root}/artifacts"
mkdir -p "${input_root}" "${build_root}" "${bundle_root}" "${artifact_root}"

npm ci --ignore-scripts --prefix apps/release/dependencies/claude-sdk >/dev/null
sdk_root="apps/release/dependencies/claude-sdk/node_modules/@anthropic-ai/claude-agent-sdk"
test -f "${sdk_root}/sdk.mjs"
test -f "${sdk_root}/LICENSE.md"

go build -trimpath -buildvcs=false -ldflags "-buildid= -s -w -X main.version=${client_version}" \
  -o "${build_root}/coconet-release" ./apps/release

node_distribution_root="https://nodejs.org/dist/v${node_version}"
curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 \
  "${node_distribution_root}/SHASUMS256.txt" \
  --output "${input_root}/SHASUMS256.txt"

client_targets=(
  "darwin amd64 x64"
  "darwin arm64 arm64"
  "linux amd64 x64"
  "linux arm64 arm64"
)
for target in "${client_targets[@]}"; do
  read -r target_os target_architecture node_architecture <<<"${target}"
  node_archive_name="node-v${node_version}-${target_os}-${node_architecture}.tar.xz"
  node_archive="${input_root}/${node_archive_name}"
  expected_node_hash="$(awk -v name="${node_archive_name}" '$2 == name { print $1 }' "${input_root}/SHASUMS256.txt")"
  if [[ ! "${expected_node_hash}" =~ ^[0-9a-f]{64}$ ]]; then
    printf 'Node release manifest omitted %s.\n' "${node_archive_name}" >&2
    exit 1
  fi
  curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 \
    "${node_distribution_root}/${node_archive_name}" --output "${node_archive}"
  actual_node_hash="$("${sha256_command[@]}" "${node_archive}" | awk '{ print $1 }')"
  if [[ "${actual_node_hash}" != "${expected_node_hash}" ]]; then
    printf 'Node archive SHA-256 mismatch for %s.\n' "${node_archive_name}" >&2
    exit 1
  fi

  target_root="${build_root}/${target_os}-${target_architecture}"
  node_root="${target_root}/node"
  mkdir -p "${node_root}"
  tar -xJf "${node_archive}" -C "${node_root}" --strip-components=1 \
    "node-v${node_version}-${target_os}-${node_architecture}/bin/node" \
    "node-v${node_version}-${target_os}-${node_architecture}/LICENSE"

  CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_architecture}" \
    build_reproducible_go_binary "${client_version}" "${target_root}/coconet" ./apps/runtime
  CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_architecture}" \
    build_reproducible_go_binary "${client_version}" "${target_root}/coconet-bootstrap" ./apps/bootstrap

  if [[ "${target_os}" == "darwin" && "${sign_macos}" == "true" ]]; then
    verify_node_macos_signature "${node_root}/bin/node"
    sign_macos_binary "${target_root}/coconet" "work.coconet.runtime"
    sign_macos_binary "${target_root}/coconet-bootstrap" "work.coconet.bootstrap"
  fi

  python3 apps/release/collect_go_licenses.py --output "${target_root}/go-licenses" \
    "${target_root}/coconet" "${target_root}/coconet-bootstrap"
  "${build_root}/coconet-release" bundle \
    --go-licenses "${target_root}/go-licenses" \
    --version "${client_version}" \
    --os "${target_os}" \
    --arch "${target_architecture}" \
    --runtime "${target_root}/coconet" \
    --bootstrap "${target_root}/coconet-bootstrap" \
    --node "${node_root}/bin/node" \
    --node-license "${node_root}/LICENSE" \
    --claude-sdk "${sdk_root}/sdk.mjs" \
    --claude-sdk-license "${sdk_root}/LICENSE.md" \
    --codex-plugin "${codex_plugin_root}" \
    --claude-plugin "${claude_plugin_root}" \
    --cursor-plugin "${cursor_plugin_root}" \
    --output "${bundle_root}" >/dev/null

  bundle_name="coconet-${client_version}-${target_os}-${target_architecture}"
  mv "${bundle_root}/${bundle_name}.tar.gz" "${artifact_root}/"
  mv "${bundle_root}/${bundle_name}.tar.gz.sha256" "${artifact_root}/"
done

server_targets=(amd64 arm64)
for target_architecture in "${server_targets[@]}"; do
  server_binary="${build_root}/coconet-server-linux-${target_architecture}"
  CGO_ENABLED=0 GOOS=linux GOARCH="${target_architecture}" \
    build_reproducible_go_binary "${server_version}" "${server_binary}" ./apps/server
  python3 apps/release/collect_go_licenses.py --output "${server_binary}.licenses" "${server_binary}"
  "${build_root}/coconet-release" server \
    --go-licenses "${server_binary}.licenses" \
    --version "${server_version}" \
    --arch "${target_architecture}" \
    --server "${server_binary}" \
    --output "${bundle_root}" >/dev/null
  bundle_name="coconet-server-${server_version}-linux-${target_architecture}"
  mv "${bundle_root}/${bundle_name}.tar.gz" "${artifact_root}/"
  mv "${bundle_root}/${bundle_name}.tar.gz.sha256" "${artifact_root}/"
done

LC_ALL=C sort "${artifact_root}"/*.sha256 >"${artifact_root}/SHA256SUMS"
mv "${artifact_root}" "${output_directory}"
printf 'Coconet release artifacts created at %s\n' "${output_directory}"
if [[ "${sign_macos}" == "true" ]]; then
  printf 'macOS Developer ID signing enabled (timestamp=%s). No notarization was submitted.\n' "${codesign_timestamp}"
fi
