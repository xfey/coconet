#!/bin/sh

data_root="${XDG_DATA_HOME:-${HOME}/.local/share}"
runtime="${data_root}/coconet/current/bin/coconet"
if [ ! -x "${runtime}" ] && command -v coconet >/dev/null 2>&1; then
  runtime="$(command -v coconet)"
fi
if [ ! -x "${runtime}" ]; then
  exit 0
fi

# This Plugin records UserPromptSubmit silently; older Plugins must never
# supply empty-session evidence to a newer Runtime.
export COCONET_HOOK_LIFECYCLE=1

if [ "${1:-}" = "--context" ]; then
  "${runtime}" hook --agent claude 2>/dev/null || :
else
  "${runtime}" hook --agent claude >/dev/null 2>&1 || :
fi
exit 0
