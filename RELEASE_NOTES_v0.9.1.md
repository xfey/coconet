# Coconet 0.9.1

Fix first-time npm installation of 0.9.0, which was rejected because release archives inherited restrictive staging directory permissions. Archives now use fixed directory modes independent of the build umask. The release build also installs its actual host-platform candidate through the npm unpacker and Bootstrap under umask 077 before packaging.

Install or upgrade with `npm install --global coconet@0.9.1`. Client, npm, both Plugins and Server use 0.9.1. Existing 0.9.0 Hosted data and metadata schema 16 remain compatible; this patch adds no Session, Library, or summary behavior changes.

The V2 features introduced in 0.9.0 remain available: Work DAG, fixed Library snapshots, topic tags, work discovery, same-Agent handoff, and a read-only Dashboard. Topic tags use visible user/assistant text; work and checkpoint summaries use bounded normalized evidence, including tool facts. Original Session artifacts remain intact.

Signed, notarized macOS archives and Linux archives are accompanied by SHA-256 manifests. Version 0.9.0 release assets remain available as historical records; use 0.9.1 for npm installation.
