# Coconet 0.11.1

Installing Coconet now configures its Plugins for the default Codex and Claude Code instances. New users can install the Runtime and begin project setup without a separate Agent registration step.

- First installation and upgrades add missing default registrations once. Existing commands for the same configuration root are preserved; additional instances remain available through `coconet agent add`.
- Missing Agent commands are skipped. Setup failures are recorded independently and do not undo the Runtime installation or another Agent's successful setup. Inspect them with `coconet agent list --verbose` and retry with `coconet agent add codex` or `coconet agent add claude`.
- Explicitly removed defaults stay removed on later upgrades. `COCONET_SKIP_AGENT_SETUP=1` skips setup for that installation. Agent Hook trust is still handled in a new Agent session; installation does not connect a project or upload Sessions.
- The Dashboard uses a minimal bilingual canvas with ELK Layered routing, rounded orthogonal connections, background layout and cached coordinates. Project Session Library shortcuts, fixed-source instruction dialogs and conversation bubbles keep the graph uncluttered.
- Library collection controls add or remove a shortcut to the fixed source; the original Session and DAG node remain available. Interface language changes do not translate Session content.

## Upgrade

```sh
npm install --global coconet@0.11.1
coconet version
coconet agent list
```

`coconet version` triggers the first Runtime installation or version upgrade through the npm launcher. Configuration roots follow `CODEX_HOME` / `CLAUDE_CONFIG_DIR`, with defaults of `~/.codex` / `~/.claude`.

Client, npm package, both Plugins and Server operator archives are version 0.11.1. Metadata remains schema 18 and existing fixed Session versions, Library sources and permissions are unchanged. Hosted users can use the new clients directly; the Dashboard is served by the Server and requires only a browser refresh.

Validation covers repository checks, isolated installation orchestration, missing/failed Agent isolation, repeat installation, explicit removal, skip options, concurrency checks, graph geometry, Worker recovery and browser interactions. Real Agent model conversations are not repeated for this installation update. The Dashboard's synthetic 50/200/500-node measurements are bounded local samples, not a general performance guarantee.
