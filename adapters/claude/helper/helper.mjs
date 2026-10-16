#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import { constants, createReadStream } from "node:fs";
import { chmod, copyFile, mkdir, open, readFile, readdir, realpath, rename, rm, stat, writeFile } from "node:fs/promises";
import { createInterface } from "node:readline";
import { basename, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import process from "node:process";

const input = await readInput();
if (typeof input?.action !== "string" || typeof input?.cwd !== "string" || typeof input?.config_root !== "string") {
  throw new Error("action, cwd, and config_root are required");
}
process.env.CLAUDE_CONFIG_DIR = input.config_root;
const sdkEntry = process.env.COCONET_CLAUDE_SDK_ENTRY
  ? resolve(process.env.COCONET_CLAUDE_SDK_ENTRY)
  : resolve(fileURLToPath(new URL("./vendor/sdk.mjs", import.meta.url)));
const sdk = await import(pathToFileURL(sdkEntry).href);

if (input.action === "fork") {
  const result = await forkImportedSession();
  process.stdout.write(`${JSON.stringify(result)}\n`);
  process.exit(0);
}

if (input.action === "verify_fork") {
  await verifyFork(input.native_session_id);
  process.stdout.write(`${JSON.stringify({ native_session_id: input.native_session_id })}\n`);
  process.exit(0);
}

if (input.action === "discover") {
  const sessions = await sdk.listSessions({ dir: input.cwd, includeProgrammatic: true, limit: 1001 });
  const result = [];
  for (const session of sessions.slice(0, 1000)) {
    if (typeof session?.sessionId !== "string" || !Number.isFinite(session.lastModified)) continue;
    let cwd = session.cwd || input.cwd;
    try { cwd = await realpath(cwd); } catch { /* Local SDK metadata remains authoritative. */ }
    result.push({
      schema_version: 1,
      agent_type: "claude",
      native_session_id: session.sessionId,
      cwd,
      activity_state: "unknown",
      ...(Number.isFinite(session.createdAt) ? { created_at: new Date(session.createdAt).toISOString() } : {}),
      updated_at: new Date(session.lastModified).toISOString(),
    });
  }
  process.stdout.write(`${JSON.stringify({ schema_version: 1, sessions: result, truncated: sessions.length > 1000 })}\n`);
  process.exit(0);
}

if (input.action !== "observe" || typeof input.native_session_id !== "string") {
  throw new Error("observe requires native_session_id");
}
// cwd is a lookup hint, never the identity of an existing Session. The SDK
// returns [] (not an error) when a scoped directory does not contain this ID.
if (!/^[A-Za-z0-9_-]+$/.test(input.native_session_id)) throw new Error("Invalid Session identity");
const artifactPath = await findArtifact(join(input.config_root, "projects"), `${input.native_session_id}.jsonl`);
if (input.transcript_path && await realpath(input.transcript_path) !== artifactPath) {
  throw new Error("Hook transcript does not match the unique Session artifact");
}
let info, messages, artifact, activeHistory, nativeMetadata;
for (let attempt = 0; attempt < 3; attempt++) {
  const before = await stat(artifactPath);
  info = await sdk.getSessionInfo(input.native_session_id);
  if (!info || info.sessionId !== input.native_session_id) throw new Error("Agent SDK did not find the requested Session");
  messages = await sdk.getSessionMessages(input.native_session_id);
  if (!Array.isArray(messages)) throw new Error("Agent SDK returned an invalid structured message chain");
  nativeMetadata = await readNativeMetadata(artifactPath);
  activeHistory = activeNativeHistory(messages, nativeMetadata);
  artifact = await hashFile(artifactPath);
  const after = await stat(artifactPath);
  if (before.dev === after.dev && before.ino === after.ino && before.size === after.size
      && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs) break;
  if (attempt === 2) throw new Error("Session artifact changed during structured observation; retry after the host finishes writing");
  await new Promise(done => setTimeout(done, 100));
}
const messageTimes = messages.map(message => typeof message.timestamp === "string" ? Date.parse(message.timestamp) : NaN);
// SDK messages exclude trailing accounting and file-history metadata. Use their
// times only when the complete chain is timestamped and its artifact stayed put.
const lastActivityAt = messageTimes.length && messageTimes.every(Number.isFinite)
  ? new Date(messageTimes.reduce((latest, time) => Math.max(latest, time), -Infinity)).toISOString() : undefined;
let cwd = info.cwd || input.cwd;
try { cwd = await realpath(cwd); } catch { /* Imported Session CWD may not exist locally. */ }
const toolCalls = new Map();
for (const message of messages) {
  const content = message.message?.content;
  if (!Array.isArray(content)) continue;
  for (const block of content) {
    if (block?.type === "tool_use" && typeof block.id === "string") toolCalls.set(block.id, block);
  }
}
const visibleTurns = [];
const turns = messages.map((message, ordinal) => {
  const text = [];
  const toolNames = new Set();
  collectText(message.message, text, toolNames);
  const normalizedText = text.join("\n");
  // The SDK preserves the host's synthetic model marker even when it omits
  // outer API-error metadata. Keep these records readable as lifecycle/UI
  // events, not as assistant-authored work evidence.
  const kind = nativeMetadata.get(message.uuid)?.summary || message.type === "assistant" && message.message?.model === "<synthetic>"
    ? "event" : messageKind(message.message);
  const role = ["user", "assistant", "system"].includes(message.type) ? message.type : "other";
  const ranges = [];
  if ((role === "user" || role === "assistant") && kind !== "event" && !message.isMeta && !message.isSidechain) {
    const content = message.message?.content;
    // Match the existing renderer's byte offsets, without descending into tool
    // results to find visible text. Preserve mixed messages and code examples.
    const prefix = [];
    if (typeof message.message?.text === "string" && message.message.text) prefix.push(message.message.text);
    let offset = Buffer.byteLength(prefix.join("\n"));
    let populated = prefix.length > 0;
    if (populated) ranges.push({ start: 0, end: offset });
    for (const block of Array.isArray(content) ? content : typeof content === "string" ? [content] : []) {
      const parts = [];
      collectText(block, parts, new Set());
      if (!parts.length) continue;
      if (populated) offset++;
      const rendered = parts.join("\n");
      const text = typeof block === "string" ? block : block?.type === "text" ? block.text : null;
      if (typeof text === "string" && text && rendered.startsWith(text)) {
        ranges.push({ start: offset, end: offset + Buffer.byteLength(text) });
      }
      offset += Buffer.byteLength(rendered);
      populated = true;
    }
  }
  const contentHash = digest([role, kind, normalizedText].join("\0"));
  if (ranges.length) visibleTurns.push({ turn_id: message.uuid, content_hash: contentHash, ranges });
  return {
    turn_id: message.uuid, ordinal, role, kind, text: normalizedText,
    ...(toolNames.size === 1 ? { tool_name: [...toolNames][0] } : {}),
    content_hash: contentHash,
  };
});
const git = typeof info.gitBranch === "string" && info.gitBranch ? { branch: info.gitBranch } : undefined;
process.stdout.write(`${JSON.stringify({
  schema_version: 1,
  session: {
    schema_version: 1,
    agent_type: "claude",
    native_session_id: input.native_session_id,
    cwd,
    activity_state: "unknown",
    source_observed_at: new Date().toISOString(),
    ...(lastActivityAt ? { last_activity_at: lastActivityAt } : {}),
    client_version: input.client_version || "unknown",
    adapter_name: "claude-agent-sdk",
    adapter_version: input.adapter_version || "1",
  },
  raw_artifact: { path: artifactPath, media_type: "application/x-ndjson", sha256: artifact.hash, size: artifact.size },
  active_history: { ids: activeHistory },
  normalized: { to_cursor: digest(""), reset: false, turns },
  visible_text: { version: 1, source_cursor: "", turns: visibleTurns },
  ...(git ? { git } : {}),
})}\n`);

// SDK supplies message bodies and the selected branch. Native metadata fills
// the pre-compaction ancestry which the SDK intentionally omits. No tool or
// message content is interpreted here, and the source file is never modified.
async function readNativeMetadata(path) {
  const entries = new Map();
  let hasEarlierMain = false;
  const lines = createInterface({ input: createReadStream(path), crlfDelay: Infinity });
  try {
    for await (const line of lines) {
      if (!line.trim()) continue;
      const row = JSON.parse(line);
      if (typeof row.uuid !== "string") continue;
      entries.set(row.uuid, {
        parent: row.parentUuid ?? null,
        logical: row.subtype === "compact_boundary" ? row.logicalParentUuid ?? null : null,
        unanchoredCompaction: row.subtype === "compact_boundary" && !row.parentUuid && !row.logicalParentUuid && hasEarlierMain,
        summary: row.isCompactSummary === true,
        main: (row.type === "user" || row.type === "assistant") && !row.isSidechain && !row.isMeta && !row.teamName,
      });
      hasEarlierMain ||= entries.get(row.uuid).main;
    }
  } finally { lines.close(); }
  return entries;
}

function activeNativeHistory(messages, entries) {
  const selected = new Set();
  if (messages.length === 0 && [...entries.values()].some(row => row.main)) {
    throw new Error("SDK returned an empty branch for a populated native transcript");
  }
  for (const message of messages) {
    let id = message.uuid;
    const visiting = new Set();
    while (id) {
      if (visiting.has(id)) throw new Error("Native branch ancestry contains a cycle");
      if (selected.has(id)) break;
      const row = entries.get(id);
      if (!row) throw new Error("Native branch ancestry is incomplete");
      if (row.unanchoredCompaction) throw new Error("Compacted native branch has no verifiable ancestor");
      visiting.add(id);
      selected.add(id);
      id = row.parent ?? row.logical;
    }
  }
  return [...entries.keys()].filter(id => selected.has(id));
}

async function readInput() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

function failArtifactLookup(code) {
  process.stdout.write(JSON.stringify({ error: { code } }) + "\n");
  process.exit(1);
}

async function findArtifact(root, expectedName) {
  const matches = [];
  async function walk(directory) {
    let entries;
    try { entries = await readdir(directory, { withFileTypes: true }); }
    catch (error) {
      if (error.code === "ENOENT") return;
      failArtifactLookup("native_session_unreadable");
    }
    for (const entry of entries) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) await walk(path);
      else if (entry.isFile() && basename(path) === expectedName) matches.push(path);
    }
  }
  await walk(root);
  if (matches.length === 0) failArtifactLookup("native_session_missing");
  if (matches.length > 1) failArtifactLookup("native_session_ambiguous");
  const canonicalRoot = await realpath(root);
  const canonical = await realpath(matches[0]);
  if (!canonical.startsWith(canonicalRoot + "/")) throw new Error("Session artifact escaped its config root");
  return canonical;
}

async function hashFile(path) {
  const metadata = await stat(path);
  const hasher = createHash("sha256");
  for await (const chunk of createReadStream(path)) hasher.update(chunk);
  return { hash: hasher.digest("hex"), size: metadata.size };
}

function digest(value) { return createHash("sha256").update(value).digest("hex"); }

function collectText(value, destination, toolNames) {
  if (typeof value === "string") { if (value) destination.push(value); return; }
  if (Array.isArray(value)) { for (const item of value) collectText(item, destination, toolNames); return; }
  if (!value || typeof value !== "object") return;
  if (value.type === "tool_use" || value.type === "tool_result") {
    const call = value.type === "tool_use" ? value : toolCalls.get(value.tool_use_id);
    if (typeof call?.name === "string") toolNames.add(call.name);
    destination.push("tool: " + JSON.stringify({
      type: value.type,
      id: value.id ?? value.tool_use_id,
      name: call?.name,
      input: call?.input,
      ...(typeof value.is_error === "boolean" ? { is_error: value.is_error } : {}),
    }));
    if ("content" in value) collectText(value.content, destination, toolNames);
    return;
  }
  if (typeof value.text === "string" && value.text) destination.push(value.text);
  if ("content" in value) collectText(value.content, destination, toolNames);
}

function messageKind(message) {
  const content = Array.isArray(message?.content) ? message.content : [];
  if (content.some((item) => item?.type === "tool_use")) return "tool_call";
  if (content.some((item) => item?.type === "tool_result")) return "tool_result";
  return "message";
}

async function forkImportedSession() {
  if (
    typeof input.native_session_id !== "string" ||
    !/^[A-Za-z0-9_-]+$/.test(input.native_session_id) ||
    typeof input.source_artifact_path !== "string" ||
    typeof input.source_artifact_sha256 !== "string" ||
    typeof input.attempt_directory !== "string"
  ) {
    throw new Error("fork requires a safe source identity, artifact, hash, and attempt directory");
  }
  const marked = await loadForkMarker();
  if (marked) {
    await verifyFork(marked.native_session_id);
    return { native_session_id: marked.native_session_id };
  }
  const source = await hashFile(input.source_artifact_path);
  if (source.hash !== input.source_artifact_sha256) throw new Error("Fork source artifact hash mismatch");
  const cwd = await realpath(input.cwd);
  const projectRoot = join(input.config_root, "projects", cwd.replace(/[^a-zA-Z0-9]/g, "-"));
  // A live local Session can have advanced beyond the selected version. Give
  // the fixed copy its own identity so SDK lookup cannot read or overwrite it.
  const idBytes = createHash("sha256").update(`coconet-claude-source/1\0${input.attempt_directory}\0${input.native_session_id}\0${source.hash}`).digest().subarray(0, 16);
  idBytes[6] = (idBytes[6] & 15) | 0x50; idBytes[8] = (idBytes[8] & 63) | 0x80;
  const idHex = idBytes.toString("hex");
  const importedID = `${idHex.slice(0,8)}-${idHex.slice(8,12)}-${idHex.slice(12,16)}-${idHex.slice(16,20)}-${idHex.slice(20)}`;
  const importedArtifact = join(projectRoot, `${importedID}.jsonl`);
  await mkdir(projectRoot, { recursive: true, mode: 0o700 });
  await mkdir(input.attempt_directory, { recursive: true, mode: 0o700 });
  const stagingPath = join(input.attempt_directory, `.claude-source-${randomUUID()}`);
  const staging = await open(stagingPath, "wx", 0o600);
  let remapped;
  try {
    const stream = createReadStream(input.source_artifact_path);
    const lines = createInterface({ input: stream, crlfDelay: Infinity });
    let bytes = 0;
    try {
      for await (const line of lines) {
        bytes += Buffer.byteLength(line) + 1;
        if (bytes > 256 * 1024 * 1024) throw new Error("Claude native source exceeds the artifact limit");
        if (!line.trim()) { await staging.writeFile("\n"); continue; }
        const record = JSON.parse(line);
        if (!record || typeof record !== "object" || Array.isArray(record)) throw new Error("Claude native source is not a JSONL record");
        // Only native routing metadata is remapped. Message UUIDs, parents,
        // tool payloads and nested user text are preserved without recursion.
        if (record.sessionId === input.native_session_id) record.sessionId = importedID;
        // A cross-device copy belongs to the destination workspace. The SDK
        // preserves this top-level routing field when forking; leaving the old
        // path here makes later observation identify the source machine's cwd.
        if (typeof record.cwd === "string") record.cwd = cwd;
        await staging.writeFile(`${JSON.stringify(record)}\n`);
      }
    } finally { lines.close(); stream.destroy(); }
    await staging.sync();
  } catch (error) { await rm(stagingPath, { force: true }); throw error; }
  finally { await staging.close(); }
  remapped = await hashFile(stagingPath);
  let imported = false;
  try {
    const existing = await hashFile(importedArtifact).catch((error) => {
      if (error?.code === "ENOENT") return null;
      throw error;
    });
    if (existing) {
      if (existing.hash !== remapped.hash) throw new Error("Claude fixed source identity has inconsistent content");
    } else {
      await copyFile(stagingPath, importedArtifact, constants.COPYFILE_EXCL);
      imported = true;
      await chmod(importedArtifact, 0o600);
    }
    const sourceMessages = await sdk.getSessionMessages(importedID, { dir: cwd });
    if (!Array.isArray(sourceMessages)) throw new Error("Agent SDK could not read the imported source Session");
    const forked = await sdk.forkSession(importedID, { dir: cwd, title: "Coconet Resume" });
    if (typeof forked?.sessionId !== "string" || forked.sessionId === input.native_session_id || forked.sessionId === importedID) {
      throw new Error("Agent SDK did not create a distinct Fork ID");
    }
    await saveForkMarker(forked.sessionId);
    await verifyFork(forked.sessionId);
    return { native_session_id: forked.sessionId };
  } finally {
    await rm(stagingPath, { force: true });
    if (imported) {
      const current = await hashFile(importedArtifact).catch(() => null);
      if (current?.hash === remapped.hash) await rm(importedArtifact, { force: true });
    }
  }
}

async function verifyFork(nativeSessionId) {
  if (typeof nativeSessionId !== "string" || !nativeSessionId) throw new Error("native Fork ID is required");
  const info = await sdk.getSessionInfo(nativeSessionId, { dir: input.cwd });
  const messages = await sdk.getSessionMessages(nativeSessionId, { dir: input.cwd });
  if (!info || info.sessionId !== nativeSessionId || !Array.isArray(messages)) {
    throw new Error("Agent SDK could not verify the new Fork");
  }
  if (info.cwd && await realpath(info.cwd).catch(() => info.cwd) !== await realpath(input.cwd)) {
    throw new Error("Agent SDK Fork belongs to a different workspace");
  }
}

async function loadForkMarker() {
  const path = join(input.attempt_directory, "native-fork.json");
  let marker;
  try { marker = JSON.parse(await readFile(path, "utf8")); } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
  if (
    marker?.schema_version !== 1 ||
    marker?.agent_type !== "claude" ||
    marker?.source_native_session_id !== input.native_session_id ||
    marker?.source_artifact_sha256 !== input.source_artifact_sha256 ||
    typeof marker?.native_session_id !== "string"
  ) {
    throw new Error("Native Fork marker does not match this Fork attempt");
  }
  return marker;
}

async function saveForkMarker(nativeSessionId) {
  await mkdir(input.attempt_directory, { recursive: true, mode: 0o700 });
  const target = join(input.attempt_directory, "native-fork.json");
  const temporary = join(input.attempt_directory, `.native-fork-${randomUUID()}`);
  const marker = {
    schema_version: 1,
    agent_type: "claude",
    source_native_session_id: input.native_session_id,
    source_artifact_sha256: input.source_artifact_sha256,
    native_session_id: nativeSessionId,
  };
  await writeFile(temporary, `${JSON.stringify(marker)}\n`, { flag: "wx", mode: 0o600 });
  await rename(temporary, target);
}
