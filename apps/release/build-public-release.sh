#!/usr/bin/env bash

set -euo pipefail

release_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${release_dir}/../.." && pwd)"
target_file="${release_dir}/public-target.json"
client_version="${1:-}"
server_version="${2:-}"
output_argument=""
sign_macos="false"
codesign_timestamp="secure"

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

shift $(( $# < 2 ? $# : 2 ))
if (( $# > 0 )) && [[ "$1" != --* ]]; then
  output_argument="$1"
  shift
fi
while (( $# > 0 )); do
  case "$1" in
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
      fail "unknown public release option: $1"
      ;;
  esac
done
if [[ "${sign_macos}" != "true" && "${codesign_timestamp}" != "secure" ]]; then
  fail "--codesign-timestamp requires --sign-macos"
fi

stable_version='^[0-9]+\.[0-9]+\.[0-9]+$'
[[ "${client_version}" =~ ${stable_version} ]] || fail "public client version must be stable x.y.z"
[[ "${server_version}" =~ ${stable_version} ]] || fail "public Server version must be stable x.y.z"
for command_name in cmp git node npm; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "required command is unavailable: ${command_name}"
done

cd "${repository_root}"
[[ -f "${target_file}" && ! -L "${target_file}" ]] || fail "public release target is unavailable"
[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || fail "public releases require a clean Git worktree"
source_revision="$(git rev-parse --verify HEAD)"
target_values="$(
  node --input-type=module - "${target_file}" <<'NODE'
import { readFileSync } from "node:fs";

const target = JSON.parse(readFileSync(process.argv[2], "utf8"));
const repositoryParts = typeof target?.repository === "string" ? target.repository.split("/") : [];
if (
  target?.schema_version !== 1 ||
  repositoryParts.length !== 2 ||
  !repositoryParts.every((part) => (
    part !== "." && part !== ".." &&
    /^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?$/u.test(part)
  )) ||
  target.tag_prefix !== "v" ||
  target.npm_package !== "coconet"
) process.exit(1);
process.stdout.write(`${target.repository}\t${target.tag_prefix}\t${target.npm_package}\n`);
NODE
)"
IFS=$'\t' read -r public_repository tag_prefix npm_package <<<"${target_values}"
[[ -n "${public_repository}" && -n "${npm_package}" ]] || fail "public release target is invalid"
release_tag="${tag_prefix}${client_version}"

if [[ -z "${output_argument}" ]]; then
  output_argument="bin/releases/${release_tag}"
fi
mkdir -p "$(dirname "${output_argument}")"
output_parent="$(cd "$(dirname "${output_argument}")" && pwd -P)"
output_directory="${output_parent}/$(basename "${output_argument}")"
[[ ! -e "${output_directory}" ]] || fail "public release output already exists: ${output_directory}"

staging_directory="${output_parent}/.$(basename "${output_directory}").public-release.$$"
[[ ! -e "${staging_directory}" ]] || fail "public release staging path already exists"
mkdir "${staging_directory}"
cleanup() {
  case "${staging_directory}" in
    "${output_parent}"/.*.public-release.*)
      if [[ -d "${staging_directory}" ]]; then
        find "${staging_directory}" -depth -delete
      fi
      ;;
  esac
}
trap cleanup EXIT

first_root="${staging_directory}/first"
repeat_root="${staging_directory}/repeat"
version_stage="${staging_directory}/version-source"
npm_stage="${staging_directory}/npm-stage"
mkdir -p "${version_stage}" "${npm_stage}/bin"
for name in README.md LICENSE cli.mjs cli.test.mjs core.mjs core.test.mjs progress.mjs progress.test.mjs package.json package-lock.json; do
  cp "installers/npm/${name}" "${npm_stage}/${name}"
done
cp "installers/npm/bin/coconet" "${npm_stage}/bin/coconet"
# cp applies the operator's umask to new files; the published launcher has a
# fixed executable mode even when release staging is private (umask 077).
chmod 0755 "${npm_stage}/bin/coconet"
cp -R "plugins/codex/coconet" "${version_stage}/codex-plugin"
cp -R "plugins/claude/coconet" "${version_stage}/claude-plugin"
cp -R "plugins/cursor/coconet" "${version_stage}/cursor-plugin"

node --input-type=module - \
  "${npm_stage}/package.json" "${npm_stage}/package-lock.json" \
  "${version_stage}/codex-plugin/.codex-plugin/plugin.json" \
  "${version_stage}/claude-plugin/.claude-plugin/plugin.json" \
  "${version_stage}/cursor-plugin/.cursor-plugin/plugin.json" \
  "${client_version}" "${public_repository}" "${npm_package}" <<'NODE'
import { readFileSync, writeFileSync } from "node:fs";

const [packagePath, lockPath, codexPath, claudePath, cursorPath, version, repository, expectedName] = process.argv.slice(2);
const packageJSON = JSON.parse(readFileSync(packagePath, "utf8"));
const lock = JSON.parse(readFileSync(lockPath, "utf8"));
const codex = JSON.parse(readFileSync(codexPath, "utf8"));
const claude = JSON.parse(readFileSync(claudePath, "utf8"));
const cursor = JSON.parse(readFileSync(cursorPath, "utf8"));
const sourceVersion = packageJSON.version;
if (
  packageJSON.name !== expectedName || packageJSON.private !== true ||
  typeof sourceVersion !== "string" || sourceVersion.length === 0 ||
  lock.name !== expectedName || lock.version !== sourceVersion ||
  lock.packages?.[""]?.name !== expectedName || lock.packages?.[""]?.version !== sourceVersion ||
  codex.name !== "coconet" || claude.name !== "coconet" || cursor.name !== "coconet" ||
  codex.version !== sourceVersion || claude.version !== sourceVersion || cursor.version !== sourceVersion
) {
  process.stderr.write("npm and Plugin source metadata is inconsistent\n");
  process.exit(1);
}
packageJSON.version = version;
delete packageJSON.private;
packageJSON.repository = { type: "git", url: `git+https://github.com/${repository}.git` };
packageJSON.homepage = `https://github.com/${repository}#readme`;
packageJSON.bugs = { url: `https://github.com/${repository}/issues` };
lock.version = version;
lock.packages[""].version = version;
codex.version = version;
claude.version = version;
cursor.version = version;
for (const [path, value] of [
  [packagePath, packageJSON], [lockPath, lock], [codexPath, codex], [claudePath, claude], [cursorPath, cursor],
]) {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o644 });
}
NODE

artifact_source_options=(
  --npm-package-json "${npm_stage}/package.json"
  --codex-plugin-root "${version_stage}/codex-plugin"
  --claude-plugin-root "${version_stage}/claude-plugin"
  --cursor-plugin-root "${version_stage}/cursor-plugin"
)
"${release_dir}/build-release-artifacts.sh" \
  "${client_version}" "${server_version}" "${first_root}" "${artifact_source_options[@]}"
"${release_dir}/build-release-artifacts.sh" \
  "${client_version}" "${server_version}" "${repeat_root}" "${artifact_source_options[@]}"

first_names="$(find "${first_root}" -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort)"
repeat_names="$(find "${repeat_root}" -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort)"
[[ "${first_names}" == "${repeat_names}" ]] || fail "repeated public builds produced different file sets"
while IFS= read -r name; do
  [[ -n "${name}" ]] || continue
  cmp -s "${first_root}/${name}" "${repeat_root}/${name}" || fail "repeated public build differed: ${name}"
done <<<"${first_names}"
find "${repeat_root}" -depth -delete
if [[ "${sign_macos}" == "true" ]]; then
  signed_root="${staging_directory}/signed"
  "${release_dir}/build-release-artifacts.sh" \
    "${client_version}" "${server_version}" "${signed_root}" \
    "${artifact_source_options[@]}" --sign-macos "--codesign-timestamp=${codesign_timestamp}"
  find "${first_root}" -depth -delete
  mv "${signed_root}"/* "${staging_directory}/"
  rmdir "${signed_root}"
else
  mv "${first_root}"/* "${staging_directory}/"
  rmdir "${first_root}"
fi
find "${version_stage}" -depth -delete

npm_manifest="${staging_directory}/npm-release-manifest.json"
node "${release_dir}/public-release.mjs" npm-manifest \
  "${staging_directory}" "${client_version}" "${server_version}" \
  "${public_repository}" "${release_tag}" "${npm_manifest}"

cp "${npm_manifest}" "${npm_stage}/release-manifest.json"

npm ci --ignore-scripts --prefix "${npm_stage}" >/dev/null
npm test --prefix "${npm_stage}"
node --input-type=module - "${npm_stage}" "${staging_directory}" "${client_version}" <<'NODE'
import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [npmRoot, artifacts, version] = process.argv.slice(2);
const { bundleName, resolveTarget } = await import(pathToFileURL(join(npmRoot, "core.mjs")));
const archive = join(artifacts, `${bundleName(version, resolveTarget())}.tar.gz`);
const installRoot = join(artifacts, "install-check");
process.umask(0o077);
for (const [command, args] of [
  [process.execPath, [join(npmRoot, "cli.mjs"), "--artifact", archive, "--checksum", `${archive}.sha256`, "--root", installRoot, "--skip-agents", "--json"]],
  [join(installRoot, "current", "bin", "coconet"), ["version"]],
]) {
  const result = spawnSync(command, args, { stdio: "inherit", timeout: 120_000 });
  if (result.error || result.status !== 0) throw new Error("candidate npm installation check failed", { cause: result.error });
}
NODE
find "${staging_directory}/install-check" -depth -delete
npm pack --ignore-scripts --pack-destination "${staging_directory}" "${npm_stage}" >/dev/null
npm_tarball="${staging_directory}/coconet-${client_version}.tgz"
[[ -f "${npm_tarball}" && ! -L "${npm_tarball}" ]] || fail "npm pack omitted the expected public tarball"
find "${npm_stage}" -depth -delete

node "${release_dir}/public-release.mjs" metadata \
  "${staging_directory}" "${client_version}" "${server_version}" \
  "${public_repository}" "${release_tag}" "${source_revision}" \
  "${npm_tarball}" "${staging_directory}/release.json" \
  "${sign_macos}" "${codesign_timestamp}"
[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || fail "public release build changed the tracked worktree"

mv "${staging_directory}" "${output_directory}"
trap - EXIT
printf 'Coconet public release candidate created at %s\n' "${output_directory}"
printf 'repository=%s tag=%s source=%s client=%s server=%s\n' \
  "${public_repository}" "${release_tag}" "${source_revision}" "${client_version}" "${server_version}"
printf 'macos_developer_id_signed=%s secure_timestamp=%s notarization_submitted=false\n' \
  "${sign_macos}" "$([[ "${sign_macos}" == "true" && "${codesign_timestamp}" == "secure" ]] && printf true || printf false)"
printf 'No GitHub asset or npm package was published.\n'
