import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { posix } from "node:path";

const safeReleaseValue = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

// SemVer ordering matches distribution.CompareVersions. Unknown development
// versions are left alone; npm installation is never an implicit downgrade.
export function compareVersions(left, right) {
  const pattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/u;
  const a = pattern.exec(left), b = pattern.exec(right);
  if (!a || !b) return null;
  const numeric = value => /^\d+$/u.test(value);
  for (const part of [a[4], b[4]]) {
    if (part?.split('.').some(id => numeric(id) && id.length > 1 && id[0] === '0')) return null;
  }
  const numberOrder = (x, y) => x.length === y.length ? (x > y ? 1 : x < y ? -1 : 0) : (x.length > y.length ? 1 : -1);
  for (let i = 1; i <= 3; i++) { const order = numberOrder(a[i], b[i]); if (order) return order; }
  if (a[4] === b[4]) return 0;
  if (!a[4]) return 1;
  if (!b[4]) return -1;
  const x = a[4].split('.'), y = b[4].split('.');
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    if (x[i] === y[i]) continue;
    const n = numeric(x[i]), m = numeric(y[i]);
    if (n && m) return numberOrder(x[i], y[i]);
    if (n !== m) return n ? -1 : 1;
    return x[i] > y[i] ? 1 : -1;
  }
  return x.length > y.length ? 1 : -1;
}

export function resolveTarget(platform = process.platform, architecture = process.arch) {
  const systems = { darwin: "darwin", linux: "linux" };
  const architectures = { x64: "amd64", arm64: "arm64", amd64: "amd64" };
  const targetOS = systems[platform];
  const targetArchitecture = architectures[architecture];
  if (!targetOS || !targetArchitecture) {
    throw new Error(`Coconet does not support ${platform}/${architecture}`);
  }
  return { os: targetOS, architecture: targetArchitecture };
}

export function bundleName(version, target) {
  if (
    !safeReleaseValue.test(version) ||
    !safeReleaseValue.test(target.os) ||
    !safeReleaseValue.test(target.architecture)
  ) {
    throw new Error("bundle version or platform is invalid");
  }
  return `coconet-${version}-${target.os}-${target.architecture}`;
}

export function parseChecksum(content, archiveName) {
  const lines = content
    .split(/\r?\n/u)
    .map((line) => line.trim())
    .filter(Boolean);
  if (lines.length !== 1) {
    throw new Error("checksum file must contain exactly one entry");
  }
  const match = /^([0-9a-fA-F]{64})\s+\*?(.+)$/u.exec(lines[0]);
  if (!match || match[2] !== archiveName) {
    throw new Error("checksum file does not match the selected archive");
  }
  return match[1].toLowerCase();
}

export function releaseChecksum(manifest, version, target) {
  if (
    manifest?.schema_version !== 2 ||
    manifest.version !== version ||
    typeof manifest.archives !== "object" ||
    manifest.archives === null ||
    Array.isArray(manifest.archives)
  ) {
    throw new Error("bundled release checksum manifest is invalid or does not match the release");
  }
  const archiveName = `${bundleName(version, target)}.tar.gz`;
  const checksum = manifest.archives[archiveName];
  if (typeof checksum !== "string" || !/^[0-9a-f]{64}$/u.test(checksum)) {
    throw new Error(`bundled release checksum is unavailable for ${archiveName}`);
  }
  return checksum;
}

export function releaseBaseURL(manifest, version) {
  if (
    manifest?.schema_version !== 2 ||
    manifest.version !== version ||
    !isGitHubRepository(manifest.repository) ||
    manifest.release_tag !== `v${version}`
  ) {
    throw new Error("bundled release location is invalid or does not match the release");
  }
  return `https://github.com/${manifest.repository}/releases/download/${manifest.release_tag}/`;
}

function isGitHubRepository(value) {
  if (typeof value !== "string") return false;
  const parts = value.split("/");
  return parts.length === 2 && parts.every((part) => (
    part !== "." && part !== ".." &&
    /^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?$/u.test(part)
  ));
}

export async function verifySHA256(path, expected, signal) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path, { signal })) {
    hash.update(chunk);
  }
  const actual = hash.digest("hex");
  if (actual !== expected.toLowerCase()) {
    throw new Error("Coconet archive SHA-256 mismatch");
  }
  return actual;
}

export function isSafeArchiveEntry(path, type, expectedRoot) {
  if (type !== "File" && type !== "Directory") {
    return false;
  }
  if (!path || path.includes("\\") || path.includes("\0") || path.startsWith("/")) {
    return false;
  }
  const normalized = path.endsWith("/") ? path.slice(0, -1) : path;
  if (!normalized || posix.normalize(normalized) !== normalized) {
    return false;
  }
  const parts = normalized.split("/");
  if (parts.some((part) => part === "" || part === "." || part === "..")) {
    return false;
  }
  return normalized === expectedRoot || normalized.startsWith(`${expectedRoot}/`);
}

export function parseInstallOptions(arguments_, packageVersion) {
  const options = {
    release: packageVersion,
    baseURL: "",
    artifact: "",
    checksum: "",
    bootstrapArguments: ["install"],
    help: false,
  };
  const args = [...arguments_];
  if (args[0] === "install") {
    args.shift();
  }
  const values = new Map([
    ["--release", "release"],
    ["--base-url", "baseURL"],
    ["--artifact", "artifact"],
    ["--checksum", "checksum"],
  ]);
  const forwardedValues = new Set([
    "--root",
    "--bin-dir",
  ]);
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === "--help" || argument === "-h") {
      options.help = true;
      continue;
    }
    if (argument === "--json" || argument === "--skip-agents" || argument === "--if-newer") {
      options.bootstrapArguments.push(argument);
      continue;
    }
    const value = args[index + 1];
    if (!value || value.startsWith("--")) {
      throw new Error(`missing value for ${argument}`);
    }
    if (values.has(argument)) {
      options[values.get(argument)] = value;
      index += 1;
      continue;
    }
    if (forwardedValues.has(argument)) {
      options.bootstrapArguments.push(argument, value);
      index += 1;
      continue;
    }
    throw new Error(`unknown setup argument ${argument}`);
  }
  if (!safeReleaseValue.test(options.release)) {
    throw new Error("release version is invalid");
  }
  if (Boolean(options.artifact) !== Boolean(options.checksum)) {
    throw new Error("--artifact and --checksum must be provided together");
  }
  if (!options.artifact && options.baseURL) {
    const baseURL = new URL(options.baseURL);
    if (baseURL.protocol !== "https:") {
      throw new Error("remote Coconet releases require HTTPS");
    }
    options.baseURL = baseURL.href.endsWith("/") ? baseURL.href : `${baseURL.href}/`;
  }
  if (options.release === packageVersion) {
    options.bootstrapArguments.splice(1, 0, "--no-bin-link");
  }
  return options;
}
