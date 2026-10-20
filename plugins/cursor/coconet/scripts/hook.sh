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
  "${runtime}" hook --agent cursor 2>/dev/null || :
else
  "${runtime}" hook --agent cursor >/dev/null 2>&1 || :
fi
exit 0
