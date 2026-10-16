import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  createNPMReleaseManifest,
  createPublicReleaseMetadata,
  publicAssetNames,
} from "./public-release.mjs";

test("public release metadata pins exact GitHub and npm assets", async () => {
  const root = await mkdtemp(join(tmpdir(), "coconet-public-release-test-"));
  try {
    const clientVersion = "1.2.3";
    const serverVersion = "2.3.4";
    const checksumLines = [];
    for (const name of publicAssetNames(clientVersion, serverVersion)) {
      const content = Buffer.from(`fixture ${name}\n`);
      const hash = sha256(content);
      await writeFile(join(root, name), content, { mode: 0o600 });
      const line = `${hash}  ${name}`;
      await writeFile(join(root, `${name}.sha256`), `${line}\n`, { mode: 0o600 });
      checksumLines.push(line);
    }
    await writeFile(join(root, "SHA256SUMS"), `${checksumLines.sort().join("\n")}\n`, { mode: 0o600 });
    const npmTarball = join(root, "coconet-1.2.3.tgz");
    await writeFile(npmTarball, "npm fixture\n", { mode: 0o600 });

    const manifestPath = join(root, "release-manifest.json");
    const manifest = await createNPMReleaseManifest({
      artifactRoot: root,
      clientVersion,
      serverVersion,
      repository: "xfey/coconet",
      releaseTag: "v1.2.3",
      output: manifestPath,
    });
    assert.equal(manifest.schema_version, 2);
    assert.equal(manifest.repository, "xfey/coconet");
    assert.equal(Object.keys(manifest.archives).length, 4);
    assert.deepEqual(JSON.parse(await readFile(manifestPath, "utf8")), manifest);

    const metadataPath = join(root, "release.json");
    const metadata = await createPublicReleaseMetadata({
      artifactRoot: root,
      clientVersion,
      serverVersion,
      repository: "xfey/coconet",
      releaseTag: "v1.2.3",
      revision: "a".repeat(40),
      npmTarball,
      output: metadataPath,
      macosDeveloperIDSigned: true,
      macosSecureTimestamp: true,
    });
    assert.equal(metadata.schema_version, 2);
    assert.equal(metadata.channel, "stable");
    assert.equal(Object.keys(metadata.archives).length, 6);
    assert.deepEqual(metadata.npm, {
      name: "coconet",
      version: "1.2.3",
      tarball: "coconet-1.2.3.tgz",
      sha256: sha256(Buffer.from("npm fixture\n")),
    });
    assert.equal(metadata.npm.sha256, sha256(Buffer.from("npm fixture\n")));
    assert.deepEqual(metadata.macos, {
      developer_id_signed: true,
      secure_timestamp: true,
    });

    const damaged = publicAssetNames(clientVersion, serverVersion)[0];
    await writeFile(join(root, damaged), "damaged\n", { mode: 0o600 });
    await assert.rejects(
      createNPMReleaseManifest({
        artifactRoot: root,
        clientVersion,
        serverVersion,
        repository: "xfey/coconet",
        releaseTag: "v1.2.3",
        output: manifestPath,
      }),
      /integrity mismatch/u,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("public release rejects prerelease versions and mismatched tags", async () => {
  assert.throws(() => publicAssetNames("1.2.3-internal.1", "2.3.4"), /stable/u);
  await assert.rejects(
    createNPMReleaseManifest({
      artifactRoot: "/tmp/unused",
      clientVersion: "1.2.3",
      serverVersion: "2.3.4",
      repository: "xfey/coconet",
      releaseTag: "latest",
      output: "/tmp/unused-output",
    }),
    /repository or tag/u,
  );
});

test("public target fixes the repository and npm identity", async () => {
  const target = JSON.parse(
    await readFile(new URL("./public-target.json", import.meta.url), "utf8"),
  );
  assert.deepEqual(target, {
    schema_version: 1,
    repository: "xfey/coconet",
    tag_prefix: "v",
    npm_package: "coconet",
  });
});

function sha256(content) {
  return createHash("sha256").update(content).digest("hex");
}
