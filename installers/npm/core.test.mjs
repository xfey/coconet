import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

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

test("supported targets map to release names", () => {
  assert.deepEqual(resolveTarget("darwin", "arm64"), { os: "darwin", architecture: "arm64" });
  assert.deepEqual(resolveTarget("linux", "x64"), { os: "linux", architecture: "amd64" });
  assert.equal(bundleName("0.4.0", resolveTarget("linux", "x64")), "coconet-0.4.0-linux-amd64");
  assert.throws(() => resolveTarget("win32", "x64"), /does not support/u);
  assert.throws(() => bundleName("../bad", { os: "linux", architecture: "amd64" }), /invalid/u);
});

test("checksum is bound to one exact archive", () => {
  const hash = "a".repeat(64);
  assert.equal(parseChecksum(`${hash}  coconet.tar.gz\n`, "coconet.tar.gz"), hash);
  assert.throws(() => parseChecksum(`${hash}  other.tar.gz\n`, "coconet.tar.gz"), /does not match/u);
  assert.throws(() => parseChecksum(`${hash}  coconet.tar.gz\n${hash}  other\n`, "coconet.tar.gz"), /exactly one/u);
});

test("remote checksum is pinned by the bundled release manifest", () => {
  const hash = "a".repeat(64);
  const manifest = {
    schema_version: 2,
    version: "0.4.0",
    repository: "xfey/coconet",
    release_tag: "v0.4.0",
    archives: { "coconet-0.4.0-linux-amd64.tar.gz": hash },
  };
  const target = { os: "linux", architecture: "amd64" };
  assert.equal(releaseChecksum(manifest, "0.4.0", target), hash);
  assert.throws(() => releaseChecksum(manifest, "0.5.0", target), /does not match/u);
  assert.throws(
    () => releaseChecksum(manifest, "0.4.0", { os: "linux", architecture: "arm64" }),
    /unavailable/u,
  );
});

test("archive entries cannot escape or link", () => {
  const root = "coconet-0.4.0-linux-amd64";
  assert.equal(isSafeArchiveEntry(`${root}/bin/coconet`, "File", root), true);
  assert.equal(isSafeArchiveEntry(`${root}/`, "Directory", root), true);
  assert.equal(isSafeArchiveEntry(`../${root}/bin/coconet`, "File", root), false);
  assert.equal(isSafeArchiveEntry(`${root}/../escape`, "File", root), false);
  assert.equal(isSafeArchiveEntry(`${root}/link`, "SymbolicLink", root), false);
  assert.equal(isSafeArchiveEntry(`/tmp/${root}`, "Directory", root), false);
});

test("install arguments separate wrapper and Bootstrap options", () => {
  const options = parseInstallOptions(
    [
      "install",
      "--release",
      "0.4.0",
      "--artifact",
      "/tmp/coconet.tar.gz",
      "--checksum",
      "/tmp/coconet.tar.gz.sha256",
      "--root",
      "/install",
      "--json",
    ],
    "0.4.0",
  );
  assert.equal(options.release, "0.4.0");
  assert.deepEqual(options.bootstrapArguments, [
    "install",
    "--no-bin-link",
    "--root",
    "/install",
    "--json",
  ]);
  assert.throws(() => parseInstallOptions(["--base-url", "http://example.com"], "0.4.0"), /HTTPS/u);
  assert.throws(() => parseInstallOptions(["--artifact", "archive"], "0.4.0"), /provided together/u);
  assert.deepEqual(parseInstallOptions(["--json"], "0.4.0").bootstrapArguments, [
    "install", "--no-bin-link", "--json",
  ]);
  assert.deepEqual(parseInstallOptions(["--skip-agents", "--json"], "0.4.0").bootstrapArguments, [
    "install", "--no-bin-link", "--skip-agents", "--json",
  ]);

  const legacy = parseInstallOptions([
    "--release", "0.3.0",
    "--artifact", "/tmp/legacy.tar.gz",
    "--checksum", "/tmp/legacy.tar.gz.sha256",
    "--bin-dir", "/isolated/bin",
  ], "0.4.0");
  assert.deepEqual(legacy.bootstrapArguments, ["install", "--bin-dir", "/isolated/bin"]);
});

test("release location is pinned to the public repository and matching tag", () => {
  const options = parseInstallOptions([], "0.4.0");
  assert.equal(options.baseURL, "");
  assert.equal(
    releaseBaseURL({
      schema_version: 2,
      version: "0.4.0",
      repository: "xfey/coconet",
      release_tag: "v0.4.0",
    }, "0.4.0"),
    "https://github.com/xfey/coconet/releases/download/v0.4.0/",
  );
  assert.throws(
    () => releaseBaseURL({
      schema_version: 2,
      version: "0.4.0",
      repository: "xfey/coconet",
      release_tag: "latest",
    }, "0.4.0"),
    /location is invalid/u,
  );
});

test("bundled package pins the final repository and its publication state", async () => {
  const manifest = JSON.parse(
    await readFile(new URL("./release-manifest.json", import.meta.url), "utf8"),
  );
  const packageJSON = JSON.parse(
    await readFile(new URL("./package.json", import.meta.url), "utf8"),
  );
  assert.equal(manifest.version, packageJSON.version);
  assert.equal(manifest.repository, "xfey/coconet");
  assert.equal(manifest.release_tag, `v${packageJSON.version}`);
  if (packageJSON.private === true) {
    assert.deepEqual(manifest.archives, {});
    assert.throws(
      () => releaseChecksum(manifest, packageJSON.version, resolveTarget("darwin", "arm64")),
      /checksum is unavailable/u,
    );
  } else {
    assert.equal(Object.keys(manifest.archives).length, 4);
    for (const [platform, architecture] of [
      ["darwin", "x64"], ["darwin", "arm64"], ["linux", "x64"], ["linux", "arm64"],
    ]) {
      assert.match(
        releaseChecksum(manifest, packageJSON.version, resolveTarget(platform, architecture)),
        /^[0-9a-f]{64}$/u,
      );
    }
  }
});

test("local archive hash is streamed and verified", async () => {
  const root = await mkdtemp(join(tmpdir(), "coconet-installer-test-"));
  try {
    const path = join(root, "artifact");
    const content = Buffer.from("verified artifact");
    await writeFile(path, content, { mode: 0o600 });
    const expected = createHash("sha256").update(content).digest("hex");
    assert.equal(await verifySHA256(path, expected), expected);
    await assert.rejects(verifySHA256(path, "b".repeat(64)), /mismatch/u);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
