#!/usr/bin/env node

import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { lstat, readFile, writeFile } from "node:fs/promises";
import { basename, join, resolve } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const stableVersion = /^\d+\.\d+\.\d+$/u;
const sourceRevision = /^[0-9a-f]{40}(?:[0-9a-f]{24})?$/u;

export function publicAssetNames(clientVersion, serverVersion) {
  assertReleaseInputs(clientVersion, serverVersion, "xfey/coconet", `v${clientVersion}`);
  return [
    `coconet-${clientVersion}-darwin-amd64.tar.gz`,
    `coconet-${clientVersion}-darwin-arm64.tar.gz`,
    `coconet-${clientVersion}-linux-amd64.tar.gz`,
    `coconet-${clientVersion}-linux-arm64.tar.gz`,
    `coconet-server-${serverVersion}-linux-amd64.tar.gz`,
    `coconet-server-${serverVersion}-linux-arm64.tar.gz`,
  ];
}

export async function createNPMReleaseManifest({
  artifactRoot,
  clientVersion,
  serverVersion,
  repository,
  releaseTag,
  output,
}) {
  assertReleaseInputs(clientVersion, serverVersion, repository, releaseTag);
  const artifacts = await verifyPublicArtifacts(artifactRoot, clientVersion, serverVersion);
  const archives = Object.fromEntries(
    Object.entries(artifacts.archives)
      .filter(([name]) => name.startsWith(`coconet-${clientVersion}-`))
      .map(([name, value]) => [name, value.sha256]),
  );
  if (Object.keys(archives).length !== 4) {
    throw new Error("public release omitted a client platform archive");
  }
  const manifest = {
    schema_version: 2,
    version: clientVersion,
    repository,
    release_tag: releaseTag,
    archives,
  };
  await writeJSON(output, manifest);
  return manifest;
}

export async function createPublicReleaseMetadata({
  artifactRoot,
  clientVersion,
  serverVersion,
  repository,
  releaseTag,
  revision,
  npmTarball,
  output,
  macosDeveloperIDSigned = false,
  macosSecureTimestamp = false,
}) {
  assertReleaseInputs(clientVersion, serverVersion, repository, releaseTag);
  if (!sourceRevision.test(revision)) {
    throw new Error("public release source revision is invalid");
  }
  const artifacts = await verifyPublicArtifacts(artifactRoot, clientVersion, serverVersion);
  await requireRegularFile(npmTarball);
  const expectedNPMName = `coconet-${clientVersion}.tgz`;
  if (basename(npmTarball) !== expectedNPMName) {
    throw new Error("public release npm tarball has an unexpected name");
  }
  const metadata = {
    schema_version: 2,
    channel: "stable",
    repository,
    release_tag: releaseTag,
    source_revision: revision,
    client_version: clientVersion,
    server_version: serverVersion,
    sha256sums_sha256: artifacts.sha256sumsSHA256,
    archives: Object.fromEntries(
      Object.entries(artifacts.archives).map(([name, value]) => [name, value.sha256]),
    ),
    npm: {
      name: "coconet",
      version: clientVersion,
      tarball: expectedNPMName,
      sha256: await hashFile(npmTarball),
    },
    macos: {
      developer_id_signed: macosDeveloperIDSigned,
      secure_timestamp: macosSecureTimestamp,
    },
  };
  await writeJSON(output, metadata);
  return metadata;
}

async function verifyPublicArtifacts(artifactRoot, clientVersion, serverVersion) {
  const names = publicAssetNames(clientVersion, serverVersion);
  const archives = {};
  const expectedChecksumLines = [];
  for (const name of names) {
    const archive = join(artifactRoot, name);
    const checksum = `${archive}.sha256`;
    await requireRegularFile(archive);
    await requireRegularFile(checksum);
    const line = (await readFile(checksum, "utf8")).trim();
    const match = /^([0-9a-f]{64})\s+\*?(.+)$/u.exec(line);
    if (!match || match[2] !== name) {
      throw new Error(`public release checksum does not match ${name}`);
    }
    const actual = await hashFile(archive);
    if (actual !== match[1]) {
      throw new Error(`public release archive integrity mismatch for ${name}`);
    }
    archives[name] = { sha256: actual };
    expectedChecksumLines.push(line);
  }
  const sumsPath = join(artifactRoot, "SHA256SUMS");
  await requireRegularFile(sumsPath);
  const sumsContent = await readFile(sumsPath, "utf8");
  const actualLines = sumsContent.split(/\r?\n/u).map((line) => line.trim()).filter(Boolean);
  if (
    actualLines.length !== expectedChecksumLines.length ||
    new Set(actualLines).size !== actualLines.length ||
    expectedChecksumLines.some((line) => !actualLines.includes(line))
  ) {
    throw new Error("public release SHA256SUMS does not contain the exact archive set");
  }
  return { archives, sha256sumsSHA256: sha256(Buffer.from(sumsContent)) };
}

function assertReleaseInputs(clientVersion, serverVersion, repository, releaseTag) {
  if (!stableVersion.test(clientVersion) || !stableVersion.test(serverVersion)) {
    throw new Error("public releases require stable client and Server versions");
  }
  if (!isGitHubRepository(repository) || releaseTag !== `v${clientVersion}`) {
    throw new Error("public release repository or tag is invalid");
  }
}

function isGitHubRepository(value) {
  if (typeof value !== "string") return false;
  const parts = value.split("/");
  return parts.length === 2 && parts.every((part) => (
    part !== "." && part !== ".." &&
    /^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?$/u.test(part)
  ));
}

async function requireRegularFile(path) {
  const metadata = await lstat(path);
  if (!metadata.isFile() || metadata.isSymbolicLink()) {
    throw new Error("public release input is not a regular file");
  }
}

async function hashFile(path) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest("hex");
}

function sha256(content) {
  return createHash("sha256").update(content).digest("hex");
}

async function writeJSON(path, value) {
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o644 });
}

async function runCLI() {
  const [command, ...args] = process.argv.slice(2);
  if (command === "npm-manifest" && args.length === 6) {
    await createNPMReleaseManifest({
      artifactRoot: args[0], clientVersion: args[1], serverVersion: args[2],
      repository: args[3], releaseTag: args[4], output: args[5],
    });
    return;
  }
  if (command === "metadata" && args.length === 10) {
    const signed = parseBoolean(args[8], "macOS signing state");
    if (args[9] !== "secure" && args[9] !== "none") {
      throw new Error("macOS timestamp mode is invalid");
    }
    await createPublicReleaseMetadata({
      artifactRoot: args[0], clientVersion: args[1], serverVersion: args[2],
      repository: args[3], releaseTag: args[4], revision: args[5],
      npmTarball: args[6], output: args[7], macosDeveloperIDSigned: signed,
      macosSecureTimestamp: signed && args[9] === "secure",
    });
    return;
  }
  throw new Error(
    "usage: public-release.mjs npm-manifest ARTIFACTS CLIENT SERVER REPOSITORY TAG OUTPUT\n" +
    "   or: public-release.mjs metadata ARTIFACTS CLIENT SERVER REPOSITORY TAG REVISION NPM_TARBALL OUTPUT MACOS_SIGNED TIMESTAMP_MODE",
  );
}

function parseBoolean(value, label) {
  if (value === "true") return true;
  if (value === "false") return false;
  throw new Error(`${label} is invalid`);
}

if (resolve(process.argv[1] || "") === fileURLToPath(import.meta.url)) {
  runCLI().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
