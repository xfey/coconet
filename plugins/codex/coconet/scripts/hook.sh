#!/bin/sh

data_root="${XDG_DATA_HOME:-${HOME}/.local/share}"
runtime="${data_root}/coconet/current/bin/coconet"
if [ ! -x "${runtime}" ] && command -v coconet >/dev/null 2>&1; then
  runtime="$(command -v coconet)"
fi
if [ ! -x "${runtime}" ]; then
  exit 0
fi

if [ "${1:-}" = "--context" ]; then
  plugin_root="$(CDPATH= cd -- "${0%/*}/.." 2>/dev/null && pwd -P)"
  "${runtime}" hook --agent codex --skill-path "${plugin_root}/skills/coconet-team-context/SKILL.md" 2>/dev/null || :
else
  "${runtime}" hook --agent codex >/dev/null 2>&1 || :
fi
exit 0
