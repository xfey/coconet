#!/usr/bin/env bash

set -euo pipefail

integration_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${integration_dir}/../.." && pwd)"
setup_cli="${repository_root}/installers/npm/cli.mjs"
previous_version=""
previous_archive=""
previous_checksum=""
current_version=""
current_archive=""
current_checksum=""

usage() {
  cat <<'EOF'
usage: tests/integration/phase7_internal_install.sh \
  --previous-version VERSION --previous-archive FILE --previous-checksum FILE \
  --current-version VERSION --current-archive FILE --current-checksum FILE \
  [--setup-cli FILE]

Runs isolated local-artifact install, repeat install, upgrade, rollback,
multi-instance Agent registration, isolated failure, removal, and final
rollback. It never calls codex or claude and never reads or writes their native
configuration roots.
EOF
}

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --previous-version) [[ $# -ge 2 ]] || fail "--previous-version requires a value"; previous_version="$2"; shift 2 ;;
    --previous-archive) [[ $# -ge 2 ]] || fail "--previous-archive requires a value"; previous_archive="$2"; shift 2 ;;
    --previous-checksum) [[ $# -ge 2 ]] || fail "--previous-checksum requires a value"; previous_checksum="$2"; shift 2 ;;
    --current-version) [[ $# -ge 2 ]] || fail "--current-version requires a value"; current_version="$2"; shift 2 ;;
    --current-archive) [[ $# -ge 2 ]] || fail "--current-archive requires a value"; current_archive="$2"; shift 2 ;;
    --current-checksum) [[ $# -ge 2 ]] || fail "--current-checksum requires a value"; current_checksum="$2"; shift 2 ;;
    --setup-cli) [[ $# -ge 2 ]] || fail "--setup-cli requires a value"; setup_cli="$2"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

version_pattern='^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
[[ "${previous_version}" =~ ${version_pattern} ]] || fail "previous version is invalid"
[[ "${current_version}" =~ ${version_pattern} ]] || fail "current version is invalid"
[[ "${previous_version}" != "${current_version}" ]] || fail "previous and current versions must differ"
for path in "${setup_cli}" "${previous_archive}" "${previous_checksum}" "${current_archive}" "${current_checksum}"; do
  [[ -f "${path}" && ! -L "${path}" ]] || fail "required regular file is unavailable: ${path}"
done
for command_name in node mktemp uname; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "required command is unavailable: ${command_name}"
done

case "$(uname -s)" in
  Darwin) target_os="darwin" ;;
  Linux) target_os="linux" ;;
  *) fail "unsupported test operating system" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) target_architecture="amd64" ;;
  arm64|aarch64) target_architecture="arm64" ;;
  *) fail "unsupported test architecture" ;;
esac

previous_name="coconet-${previous_version}-${target_os}-${target_architecture}.tar.gz"
current_name="coconet-${current_version}-${target_os}-${target_architecture}.tar.gz"
[[ "$(basename "${previous_archive}")" == "${previous_name}" ]] || fail "previous archive does not target this platform"
[[ "$(basename "${previous_checksum}")" == "${previous_name}.sha256" ]] || fail "previous checksum filename is invalid"
[[ "$(basename "${current_archive}")" == "${current_name}" ]] || fail "current archive does not target this platform"
[[ "$(basename "${current_checksum}")" == "${current_name}.sha256" ]] || fail "current checksum filename is invalid"

test_temporary_parent="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
test_root="$(mktemp -d "${test_temporary_parent}/coconet-internal-install.XXXXXX")"
case "${test_root}" in
  "${test_temporary_parent}"/coconet-internal-install.*) ;;
  *) fail "temporary root escaped its safety boundary" ;;
esac
cleanup() {
  case "${test_root}" in
    "${test_temporary_parent}"/coconet-internal-install.*)
      if [[ -d "${test_root}" ]]; then
        find "${test_root}" -depth -delete
      fi
      ;;
  esac
}
trap cleanup EXIT

install_root="${test_root}/install"
result_root="${test_root}/results"
mock_root="${test_root}/mock-bin"
mkdir -p "${result_root}" "${mock_root}"
failing_codex_fixture="${mock_root}/codex-fixture-internal-fail"
working_codex_fixture="${mock_root}/codex-fixture-internal-ok"
cat >"${failing_codex_fixture}" <<'MOCK'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'codex-fixture internal install verifier\n'
  exit 0
fi
printf 'intentional isolated Plugin failure\n' >&2
exit 1
MOCK
chmod 0700 "${failing_codex_fixture}"
cat >"${working_codex_fixture}" <<'MOCK'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'codex-fixture internal install verifier\n'
  exit 0
fi
state="${CODEX_HOME:?CODEX_HOME is required}/coconet-test-installed"
case "${1:-} ${2:-}" in
  "plugin add") mkdir -p "${CODEX_HOME}"; : >"${state}"; printf '{}\n' ;;
  "plugin remove") rm -f "${state}"; printf '{}\n' ;;
  "plugin list")
    if [ -f "${state}" ]; then
      printf '{"installed":[{"pluginId":"coconet@coconet","installed":true,"enabled":true}]}\n'
    else
      printf '{"installed":[]}\n'
    fi
    ;;
  *) printf '{}\n' ;;
esac
MOCK
chmod 0700 "${working_codex_fixture}"

assert_result() {
  local path="$1"
  local expected_status="$2"
  local expected_version="$3"
  local expected_already="$4"
  node --input-type=module - "${path}" "${expected_status}" "${expected_version}" "${expected_already}" <<'NODE'
import { readFileSync } from "node:fs";

const [path, expectedStatus, expectedVersion, expectedAlready] = process.argv.slice(2);
const result = JSON.parse(readFileSync(path, "utf8"));
if (
  result.status !== expectedStatus ||
  result.version !== expectedVersion ||
  result.already_exists !== (expectedAlready === "true")
) {
  throw new Error(`unexpected Bootstrap result: ${JSON.stringify(result)}`);
}
NODE
}

assert_runtime_version() {
  local expected="$1"
  local actual
  [[ "$(readlink "${test_root}/bin/coconet")" == "${install_root}/current/bin/coconet" ]] || fail "test command link escaped its isolated install"
  actual="$("${test_root}/bin/coconet" version)"
  [[ "${actual}" == "coconet ${expected}" ]] || fail "active Runtime version mismatch: ${actual}"
}

run_install() {
  local label="$1"
  local release_version="$2"
  local archive="$3"
  local checksum="$4"
  local expected_exit="$5"
  local expected_status="$6"
  local expected_already="$7"
  local stdout_path="${result_root}/${label}.json"
  local stderr_path="${result_root}/${label}.stderr"
  local exit_code

  set +e
  COCONET_SKIP_AGENT_SETUP=1 node "${setup_cli}" install \
    --release "${release_version}" \
    --artifact "${archive}" \
    --checksum "${checksum}" \
    --root "${install_root}" \
    --bin-dir "${test_root}/bin" \
    --json >"${stdout_path}" 2>"${stderr_path}"
  exit_code=$?
  set -e
  if [[ "${exit_code}" != "${expected_exit}" ]]; then
    sed -n '1,80p' "${stderr_path}" >&2
    fail "${label} returned ${exit_code}, expected ${expected_exit}"
  fi
  assert_result "${stdout_path}" "${expected_status}" "${release_version}" "${expected_already}"
}

run_install first "${previous_version}" "${previous_archive}" "${previous_checksum}" 0 ok false
assert_runtime_version "${previous_version}"
run_install repeat "${previous_version}" "${previous_archive}" "${previous_checksum}" 0 ok true
assert_runtime_version "${previous_version}"
run_install upgrade "${current_version}" "${current_archive}" "${current_checksum}" 0 ok false
assert_runtime_version "${current_version}"
[[ -d "${install_root}/versions/${previous_version}-${target_os}-${target_architecture}" ]] || fail "upgrade removed the previous release"
run_install rollback "${previous_version}" "${previous_archive}" "${previous_checksum}" 0 ok true
assert_runtime_version "${previous_version}"
run_install activate-current "${current_version}" "${current_archive}" "${current_checksum}" 0 ok true
assert_runtime_version "${current_version}"

runtime="${install_root}/current/bin/coconet"
state_root="${test_root}/state/coconet"
set +e
COCONET_HOME="${state_root}" "${runtime}" agent add codex --command "${failing_codex_fixture}" --config-root "${test_root}/agents/failed" --json >"${result_root}/agent-failed.json"
failed_exit=$?
set -e
[[ "${failed_exit}" == "1" ]] || fail "failed Agent registration returned ${failed_exit}, expected 1"

COCONET_HOME="${state_root}" "${runtime}" agent add codex --command "${working_codex_fixture}" --config-root "${test_root}/agents/one" --json >"${result_root}/agent-one.json"
working_codex_fixture_two="${mock_root}/codex-fixture-internal-ok-two"
cp "${working_codex_fixture}" "${working_codex_fixture_two}"
chmod 0700 "${working_codex_fixture_two}"
COCONET_HOME="${state_root}" "${runtime}" agent add codex --command "${working_codex_fixture_two}" --config-root "${test_root}/agents/two" --json >"${result_root}/agent-two.json"
COCONET_HOME="${state_root}" "${runtime}" agent list --json >"${result_root}/agent-list.json"
node --input-type=module - "${result_root}/agent-list.json" "${current_version}" <<'NODE'
import { readFileSync } from "node:fs";
const [path, version] = process.argv.slice(2);
const state = JSON.parse(readFileSync(path, "utf8"));
if (state.integrations.length !== 3) throw new Error(`unexpected integration count: ${state.integrations.length}`);
const configured = state.integrations.filter((entry) => entry.status === "configured" && entry.runtime_version === version);
const failed = state.integrations.filter((entry) => entry.status === "failed");
if (configured.length !== 2 || failed.length !== 1) throw new Error(`unexpected integration state: ${JSON.stringify(state)}`);
NODE

first_id="$(node --input-type=module - "${result_root}/agent-one.json" <<'NODE'
import { readFileSync } from "node:fs";
process.stdout.write(JSON.parse(readFileSync(process.argv[2], "utf8")).integration.id);
NODE
)"
COCONET_HOME="${state_root}" "${runtime}" agent remove "${first_id}" --json >"${result_root}/agent-removed.json"
COCONET_HOME="${state_root}" "${runtime}" agent list --json >"${result_root}/agent-list-after-remove.json"
node --input-type=module - "${result_root}/agent-list-after-remove.json" <<'NODE'
import { readFileSync } from "node:fs";
const state = JSON.parse(readFileSync(process.argv[2], "utf8"));
if (state.integrations.length !== 2) throw new Error(`unexpected integration count after remove: ${state.integrations.length}`);
NODE

run_install final-rollback "${previous_version}" "${previous_archive}" "${previous_checksum}" 0 ok true
assert_runtime_version "${previous_version}"
[[ -d "${install_root}/versions/${current_version}-${target_os}-${target_architecture}" ]] || fail "rollback removed the newer release"

printf 'internal install verification passed: %s -> %s -> %s\n' "${previous_version}" "${current_version}" "${previous_version}"
