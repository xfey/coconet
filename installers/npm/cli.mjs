#!/usr/bin/env node

import { createReadStream, createWriteStream } from "node:fs";
import { chmod, lstat, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
import { Readable, Transform } from "node:stream";
import { pipeline } from "node:stream/promises";
import { spawn } from "node:child_process";
import * as tar from "tar";
import { SetupProgress } from "./progress.mjs";

import {
  bundleName,
  isSafeArchiveEntry,
  parseChecksum,
  parseInstallOptions,
  releaseBaseURL,
  releaseChecksum,
  resolveTarget,
  verifySHA256,
} from "./core.mjs";

const packageMetadata = JSON.parse(await readFile(new URL("./package.json", import.meta.url), "utf8"));
const packageVersion = packageMetadata.version;
const releaseManifest = JSON.parse(
  await readFile(new URL("./release-manifest.json", import.meta.url), "utf8"),
);
const maximumArchiveBytes = 512 * 1024 * 1024;

async function main() {
  const arguments_ = process.argv.slice(2);
  if (arguments_.length === 1 && ["version", "--version", "-v"].includes(arguments_[0])) {
    process.stdout.write(`coconet ${packageVersion}\n`);
    return 0;
  }
  let options;
  try {
    options = parseInstallOptions(arguments_, packageVersion);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    printUsage();
    return 2;
  }
  if (options.help) {
    printUsage(process.stdout);
    return 0;
  }

  const target = resolveTarget();
  const rootName = bundleName(options.release, target);
  const archiveName = `${rootName}.tar.gz`;
  const progress = new SetupProgress();
  const cancellation = new AbortController();
  const { signal } = cancellation;
  let interruptedSignal;
  const signalHandlers = ["SIGINT", "SIGTERM"].map((name) => {
    const handler = () => {
      interruptedSignal = name;
      progress.stop();
      cancellation.abort();
    };
    process.on(name, handler);
    return [name, handler];
  });
  let temporaryRoot;
  try {
    temporaryRoot = await mkdtemp(join(tmpdir(), "coconet-installer-"));
    signal.throwIfAborted();
    const archivePath = join(temporaryRoot, archiveName);
    let expected;
    if (options.artifact) {
      progress.stage(`Reading Coconet ${options.release} archive...`);
      const checksumPath = join(temporaryRoot, `${archiveName}.sha256`);
      await copyVerifiedInput(resolve(options.artifact), archivePath, maximumArchiveBytes, signal);
      await copyVerifiedInput(resolve(options.checksum), checksumPath, 16 * 1024, signal);
      expected = parseChecksum(await readFile(checksumPath, "utf8"), archiveName);
    } else {
      expected = releaseChecksum(releaseManifest, options.release, target);
      const baseURL = options.baseURL || releaseBaseURL(releaseManifest, options.release);
      progress.stage(`Downloading Coconet ${options.release}...`);
      await downloadFile(new URL(archiveName, baseURL), archivePath, maximumArchiveBytes, signal, progress);
    }
    signal.throwIfAborted();
    progress.stage("Verifying archive...");
    await verifySHA256(archivePath, expected, signal);

    signal.throwIfAborted();
    progress.stage("Unpacking archive...");
    const extractedRoot = join(temporaryRoot, "extracted");
    await mkdir(extractedRoot, { mode: 0o700 });
    const archiveModes = [];
    await tar.x({
      file: archivePath,
      cwd: extractedRoot,
      strict: true,
      preservePaths: false,
      chmod: true,
      processUmask: 0,
      umask: 0,
      filter(path, entry) {
        // tar invokes filters outside its Promise. Skip new entries on
        // cancellation and let pending writes finish before cleanup.
        if (signal.aborted) return false;
        if (!isSafeArchiveEntry(path, entry.type, rootName)) {
          throw new Error(`Coconet archive contains unsafe entry ${path}`);
        }
        archiveModes.push(validatedArchiveMode(path, entry.type, entry.mode, rootName));
        return true;
      },
    });
    signal.throwIfAborted();
    await restoreArchiveModes(extractedRoot, archiveModes);
    const bundleRoot = join(extractedRoot, rootName);
    await requireRegularFile(join(bundleRoot, "manifest.json"));
    const bootstrap = join(bundleRoot, "bin", "coconet-bootstrap");
    await requireRegularFile(bootstrap);
    signal.throwIfAborted();
    progress.stage(options.bootstrapArguments.includes("--skip-agents") || process.env.COCONET_SKIP_AGENT_SETUP === "1"
      ? "Installing Coconet..."
      : "Installing Coconet and configuring Agent integrations...");
    const code = await runBootstrap(bootstrap, [
      ...options.bootstrapArguments,
      "--bundle",
      bundleRoot,
    ], signal, progress);
    signal.throwIfAborted();
    return code;
  } catch (error) {
    if (interruptedSignal) {
      process.stderr.write("coconet: Setup cancelled.\n");
      return interruptedSignal === "SIGINT" ? 130 : 143;
    }
    throw error;
  } finally {
    progress.stop();
    if (temporaryRoot) await rm(temporaryRoot, { recursive: true, force: true });
    for (const [name, handler] of signalHandlers) process.removeListener(name, handler);
  }
}

function validatedArchiveMode(path, type, value, rootName) {
  const normalizedPath = path.endsWith("/") ? path.slice(0, -1) : path;
  const mode = Number(value);
  const accepted = Number.isInteger(mode) && mode >= 0 && mode <= 0o777
    && (type === "File"
    ? mode === 0o644 || mode === 0o755
    : mode === (normalizedPath === rootName ? 0o700 : 0o755));
  if (!accepted) {
    throw new Error(`Coconet archive contains unsafe mode for ${path}`);
  }
  return { path: normalizedPath, type, mode };
}

async function restoreArchiveModes(extractedRoot, entries) {
  // File creation always applies the caller's process umask, even when tar's
  // chmod option is enabled. The archive is hash-pinned and every mode above
  // is allow-listed, so restore files first and directories deepest-first.
  const ordered = [...entries].sort((left, right) => {
    if (left.type !== right.type) return left.type === "File" ? -1 : 1;
    return right.path.split("/").length - left.path.split("/").length;
  });
  for (const entry of ordered) {
    await chmod(join(extractedRoot, entry.path), entry.mode);
  }
}

async function copyVerifiedInput(source, destination, maximumBytes, signal) {
  const info = await lstat(source);
  if (!info.isFile() || info.isSymbolicLink() || info.size > maximumBytes) {
    throw new Error(`Coconet setup input is not an accepted regular file: ${basename(source)}`);
  }
  await pipeline(
    createReadStream(source),
    createSizeGuard(maximumBytes),
    createWriteStream(destination, { mode: 0o600 }),
    { signal },
  );
}

async function downloadFile(url, destination, maximumBytes, signal, progress) {
  if (url.protocol !== "https:") {
    throw new Error("remote Coconet releases require HTTPS");
  }
  const response = await fetch(url, {
    redirect: "follow", signal: AbortSignal.any([signal, AbortSignal.timeout(60_000)]),
  });
  if (!response.ok || !response.body) {
    throw new Error(`failed to download ${url.href}: HTTP ${response.status}`);
  }
  if (new URL(response.url).protocol !== "https:") {
    throw new Error("Coconet release download redirected away from HTTPS");
  }
  const total = Number(response.headers.get("content-length"));
  await pipeline(
    Readable.fromWeb(response.body),
    createSizeGuard(maximumBytes, (bytes) => progress.download(bytes, total)),
    createWriteStream(destination, { mode: 0o600 }),
    { signal },
  );
}

function createSizeGuard(maximumBytes, onProgress = () => {}) {
  let bytes = 0;
  return new Transform({
    transform(chunk, _encoding, callback) {
      bytes += chunk.length;
      if (bytes > maximumBytes) {
        callback(new Error("Coconet setup download exceeds the allowed size"));
        return;
      }
      onProgress(bytes);
      callback(null, chunk);
    },
  });
}

async function requireRegularFile(path) {
  const info = await lstat(path);
  if (!info.isFile() || info.isSymbolicLink()) {
    throw new Error("Coconet archive omitted a required regular file");
  }
}

function runBootstrap(command, arguments_, signal, progress) {
  return new Promise((resolveExit, reject) => {
    const child = spawn(command, arguments_, {
      stdio: ["inherit", "pipe", "pipe"], env: process.env, signal,
    });
    // Bootstrap emits its result after installation and Agent setup. Stop
    // animation before forwarding any output, including warnings or JSON.
    for (const [source, destination] of [[child.stdout, process.stdout], [child.stderr, process.stderr]]) {
      source.once("data", () => progress.stop());
      source.pipe(destination, { end: false });
    }
    let spawnError;
    child.once("error", (error) => { spawnError = error; });
    child.once("close", (code, childSignal) => {
      if (spawnError) {
        reject(spawnError);
        return;
      }
      if (childSignal) {
        reject(new Error(`coconet-bootstrap terminated by ${childSignal}`));
        return;
      }
      resolveExit(code ?? 1);
    });
  });
}

function printUsage(output = process.stderr) {
  output.write(
    "usage: coconet internal-setup [install] [--release VERSION] [--base-url HTTPS_URL | --artifact FILE --checksum FILE] [--skip-agents] [--if-newer] [--json] [--root DIR] [--bin-dir DIR]\n",
  );
}

try {
  process.exitCode = await main();
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
}
