import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import * as tar from "tar";

import { bundleName, resolveTarget } from "./core.mjs";

const packageRoot = fileURLToPath(new URL(".", import.meta.url));
const cliPath = fileURLToPath(new URL("./cli.mjs", import.meta.url));
const launcherPath = fileURLToPath(new URL("./bin/coconet", import.meta.url));

test("the npm package exposes only the stable launcher", async () => {
  const packageJSON = JSON.parse(await readFile(join(packageRoot, "package.json"), "utf8"));
  assert.deepEqual(packageJSON.bin, { coconet: "./bin/coconet" });
  assert.equal((await lstat(launcherPath)).mode & 0o777, 0o755);
});

test("archive modes are restored under a restrictive umask", { timeout: 60_000 }, async () => {
  const temporaryRoot = await mkdtemp(join(tmpdir(), "coconet-umask-test-"));
  try {
    const packageJSON = JSON.parse(await readFile(join(packageRoot, "package.json"), "utf8"));
    const rootName = bundleName(packageJSON.version, resolveTarget());
    const archiveSource = join(temporaryRoot, "archive-source");
    const bundleRoot = join(archiveSource, rootName);
    const bundleBin = join(bundleRoot, "bin");
    await mkdir(bundleBin, { recursive: true, mode: 0o755 });
    await chmod(archiveSource, 0o755);
    await chmod(bundleRoot, 0o700);
    await chmod(bundleBin, 0o755);

    const manifestPath = join(bundleRoot, "manifest.json");
    const bootstrapPath = join(bundleBin, "coconet-bootstrap");
    await writeFile(manifestPath, "{}\n", { mode: 0o644 });
    await writeFile(
      bootstrapPath,
      `#!/usr/bin/env node
const { statSync } = require("node:fs");
const { dirname, join } = require("node:path");
const root = dirname(dirname(__filename));
const manifestMode = statSync(join(root, "manifest.json")).mode & 0o777;
const bootstrapMode = statSync(__filename).mode & 0o777;
const rootMode = statSync(root).mode & 0o777;
const binMode = statSync(dirname(__filename)).mode & 0o777;
if (manifestMode !== 0o644 || bootstrapMode !== 0o755 || rootMode !== 0o700 || binMode !== 0o755) {
  process.stderr.write(JSON.stringify({ manifestMode, bootstrapMode, rootMode, binMode }) + "\\n");
  process.exit(65);
}
process.stdout.write(JSON.stringify({ manifestMode, bootstrapMode, rootMode, binMode }) + "\\n");
`,
      { mode: 0o755 },
    );
    await chmod(manifestPath, 0o644);
    await chmod(bootstrapPath, 0o755);

    const archivePath = join(temporaryRoot, `${rootName}.tar.gz`);
    await tar.c({
      cwd: archiveSource,
      file: archivePath,
      gzip: true,
      noMtime: true,
    }, [rootName]);
    const hash = createHash("sha256").update(await readFile(archivePath)).digest("hex");
    const checksumPath = `${archivePath}.sha256`;
    await writeFile(checksumPath, `${hash}  ${rootName}.tar.gz\n`, { mode: 0o600 });

    const result = await run("/bin/sh", [
      "-c",
      "umask 077; exec \"$@\"",
      "coconet-umask-test",
      process.execPath,
      cliPath,
      "--artifact",
      archivePath,
      "--checksum",
      checksumPath,
      "--json",
      "--root",
      join(temporaryRoot, "install"),
    ]);
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stderr, /Reading Coconet.*Verifying archive.*Unpacking archive.*Installing Coconet/su);
    assert.doesNotMatch(result.stderr, /\x1b|\r/u);
    assert.deepEqual(JSON.parse(result.stdout), {
      manifestMode: 0o644,
      bootstrapMode: 0o755,
      rootMode: 0o700,
      binMode: 0o755,
    });
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

test("the launcher delegates matching Runtime arguments without running setup", async () => {
  const temporaryRoot = await mkdtemp(join(tmpdir(), "coconet-launcher-match-"));
  try {
    const fixture = await createLauncherFixture(temporaryRoot);
    const dataRoot = join(temporaryRoot, "data");
    await seedRuntime(dataRoot, fixture.version);
    const trace = join(temporaryRoot, "trace");
    const result = await run(fixture.launcher, ["status", "--json"], {
      env: launcherEnvironment(dataRoot, trace, "fail"),
    });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(result.stdout, "runtime <status> <--json>\n");
    assert.equal(result.stderr, "");
    await assert.rejects(readFile(trace, "utf8"), { code: "ENOENT" });
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

test("the launcher installs a missing or older Runtime before delegation", async () => {
  for (const initialVersion of ["", "0.1.0"]) {
    const temporaryRoot = await mkdtemp(join(tmpdir(), "coconet-launcher-setup-"));
    try {
      const fixture = await createLauncherFixture(temporaryRoot);
      const dataRoot = join(temporaryRoot, "data");
      if (initialVersion) await seedRuntime(dataRoot, initialVersion);
      const trace = join(temporaryRoot, "trace");
      const result = await run(fixture.launcher, ["connect", "project-code"], {
        env: launcherEnvironment(dataRoot, trace, "install"),
      });
      assert.equal(result.code, 0, result.stderr);
      assert.equal(result.stdout, "runtime <connect> <project-code>\n");
      assert.match(result.stderr, initialVersion ? /Updating Coconet 0\.1\.0 -> 7\.8\.9/u : /Installing Coconet 7\.8\.9/u);
      assert.ok(result.stderr.indexOf("before running your command") < result.stderr.indexOf("fixture setup output"));
      assert.equal(await readFile(trace, "utf8"), "setup\n");
    } finally {
      await rm(temporaryRoot, { recursive: true, force: true });
    }
  }
});

test("the launcher never downgrades or replaces unknown development versions", async () => {
  for (const version of ["99.0.0", "dev", "7.8.9+build.2"]) {
    const root = await mkdtemp(join(tmpdir(), "coconet-independent-version-"));
    try {
      const fixture = await createLauncherFixture(root);
      const data = join(root, "data"), trace = join(root, "trace");
      await seedRuntime(data, version);
      const result = await run(fixture.launcher, ["mcp"], {env:launcherEnvironment(data,trace,"fail")});
      assert.equal(result.code,0,result.stderr);assert.equal(result.stderr,"");
      assert.equal(result.stdout,"runtime <mcp>\n");
      await assert.rejects(readFile(trace),{code:"ENOENT"});
    } finally { await rm(root,{recursive:true,force:true}); }
  }
});

test("the launcher preserves setup failures and rejects incomplete setup", async () => {
  for (const [mode, expectedCode] of [["fail", 42], ["incomplete", 1]]) {
    const temporaryRoot = await mkdtemp(join(tmpdir(), "coconet-launcher-failure-"));
    try {
      const fixture = await createLauncherFixture(temporaryRoot);
      const dataRoot = join(temporaryRoot, "data");
      const trace = join(temporaryRoot, "trace");
      const result = await run(fixture.launcher, [], {
        env: launcherEnvironment(dataRoot, trace, mode),
      });
      assert.equal(result.code, expectedCode, result.stderr);
      assert.equal(await readFile(trace, "utf8"), "setup\n");
      if (mode === "incomplete") assert.match(result.stderr, /without activating/u);
      assert.equal(result.stdout, "");
    } finally {
      await rm(temporaryRoot, { recursive: true, force: true });
    }
  }
});

test("a temporary global install runs coconet immediately in the same shell", { timeout: 60_000 }, async () => {
  const temporaryRoot = await mkdtemp(join(tmpdir(), "coconet-global-test-"));
  try {
    const fixture = await createLauncherFixture(temporaryRoot);
    const globalRoot = join(temporaryRoot, "global");
    const dataRoot = join(temporaryRoot, "data");
    const trace = join(temporaryRoot, "trace");
    const result = await run("/bin/sh", [
      "-c",
      "PATH=\"$1/bin:$PATH\"; export PATH; npm install --global --ignore-scripts --prefix \"$1\" \"$2\" >/dev/null && exec coconet same-shell",
      "coconet-global-test",
      globalRoot,
      fixture.root,
    ], {
      env: launcherEnvironment(dataRoot, trace, "install", join(temporaryRoot, "npm-cache")),
    });
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stdout, /runtime <same-shell>\n$/u);
    assert.equal(await readFile(trace, "utf8"), "setup\n");
    const launcherInfo = await lstat(join(globalRoot, "bin", "coconet"));
    assert.equal(launcherInfo.isFile() || launcherInfo.isSymbolicLink(), true);
	await assert.rejects(lstat(join(globalRoot, "bin", "coconet-setup")), { code: "ENOENT" });
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

test("automatic download feedback preserves command output and reports failures", { timeout: 30_000 }, async () => {
  for (const mode of ["success", "unknown-size", "http-error", "checksum-error"]) {
    const root = await mkdtemp(join(tmpdir(), "coconet-download-feedback-"));
    try {
      const fixture = await createDownloadFixture(root, mode);
      const result = await run(fixture.launcher, ["health", "--json"], { env: fixture.env });
      assert.match(result.stderr, /Installing Coconet 7\.8\.9 before running/u);
      assert.match(result.stderr, /Downloading Coconet 7\.8\.9/u);
      if (mode === "success" || mode === "unknown-size") {
        assert.equal(result.code, 0, result.stderr);
        assert.deepEqual(JSON.parse(result.stdout), { status: "ok" });
        assert.match(result.stderr, /Verifying archive.*Unpacking archive.*Installing Coconet.*fixture installed/su);
        assert.match(result.stderr, mode === "success" ? /MB, \d+%/u : /MB downloaded/u);
        // No animation may follow the Bootstrap result or the original command.
        assert.doesNotMatch(result.stderr.slice(result.stderr.indexOf("fixture installed")), /\x1b/u);
      } else {
        assert.equal(result.code, 1);
        assert.equal(result.stdout, "");
        assert.match(result.stderr, mode === "http-error" ? /HTTP 503/u : /SHA-256 mismatch/u);
        assert.match(result.stderr, /your command was not run/u);
        await assert.rejects(lstat(join(root, "data/coconet/current")), { code: "ENOENT" });
      }
      assert.deepEqual(await readdir(fixture.temporary), []);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  }
});

test("cancelling setup clears feedback, stops the child, and never runs the requested command", { timeout: 15_000 }, async () => {
  for (const [mode, signal, code, marker] of [
    ["wait-download", "SIGINT", 130, "Downloading Coconet"],
    ["success", "SIGINT", 130, "Unpacking archive"],
    ["wait-bootstrap", "SIGTERM", 143, "fixture bootstrap waiting"],
  ]) {
    const root = await mkdtemp(join(tmpdir(), "coconet-setup-cancel-"));
    try {
      const fixture = await createDownloadFixture(root, mode);
      let cancelled = false;
      const result = await run(process.execPath, [join(fixture.root, "cli.mjs")], {
        env: fixture.env,
        onStderr(chunk, child) {
          if (!cancelled && chunk.includes(marker)) {
            cancelled = true;
            child.kill(signal);
          }
        },
      });
      assert.equal(cancelled, true);
      assert.equal(result.code, code, result.stderr);
      assert.equal(result.stdout, "");
      assert.match(result.stderr, /Setup cancelled/u);
      assert.doesNotMatch(result.stderr.slice(result.stderr.indexOf("Setup cancelled")), /\x1b/u);
      assert.deepEqual(await readdir(fixture.temporary), []);
      await assert.rejects(lstat(join(root, "data/coconet/current")), { code: "ENOENT" });
      if (mode === "wait-bootstrap") {
        const pid = Number(await readFile(join(root, "bootstrap-pid"), "utf8"));
        assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
      }
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  }
});

test("an offline upgrade keeps the original command usable and retries next launch", async () => {
  const root = await mkdtemp(join(tmpdir(), "coconet-upgrade-retry-"));
  try {
    const fixture = await createLauncherFixture(root);
    const data = join(root, "data"), trace = join(root, "trace");
    await seedRuntime(data, "0.30.1");
    let result = await run(fixture.launcher, ["status", "--json"], {env:launcherEnvironment(data, trace, "fail")});
    assert.equal(result.code, 0, result.stderr);
    assert.equal(result.stdout, "runtime <status> <--json>\n");
    assert.match(result.stderr, /next launch will retry/u);
    assert.equal(JSON.parse(await readFile(join(data, "coconet/current/manifest.json"))).version, "0.30.1");
    result = await run(fixture.launcher, ["version"], {env:launcherEnvironment(data, trace, "install")});
    assert.equal(result.code, 0, result.stderr);
    assert.equal(JSON.parse(await readFile(join(data, "coconet/current/manifest.json"))).version, fixture.version);
    assert.equal(await readFile(trace,"utf8"), "setup\nsetup\n");
  } finally { await rm(root,{recursive:true,force:true}); }
});

test("automatic upgrade preserves MCP stdin and stdout", async () => {
  const root = await mkdtemp(join(tmpdir(), "coconet-upgrade-stdio-"));
  try {
    const fixture = await createDownloadFixture(root, "success");
    await seedRuntime(join(root,"data"), "0.30.1");
    // The newly activated fixture returns JSON; follow it with stdin to prove
    // no setup subprocess consumed the incoming MCP payload.
    const setup = join(fixture.root,"cli.mjs");
    await writeFile(setup, `#!/bin/sh
mkdir -p "$XDG_DATA_HOME/coconet/current/bin"
printf '  "version": "7.8.9"\\n' > "$XDG_DATA_HOME/coconet/current/manifest.json"
printf '#!/bin/sh\\ncat\\n' > "$XDG_DATA_HOME/coconet/current/bin/coconet"
chmod +x "$XDG_DATA_HOME/coconet/current/bin/coconet"
`);
    const payload = '{"jsonrpc":"2.0","id":1,"method":"initialize"}\n';
    const result = await run(fixture.launcher,["mcp"],{env:fixture.env,stdin:payload});
    assert.equal(result.code,0,result.stderr);
    assert.equal(result.stdout,payload);
  } finally { await rm(root,{recursive:true,force:true}); }
});

test("legacy explicit update installs once without delegating a second update", async () => {
  const root = await mkdtemp(join(tmpdir(), "coconet-legacy-update-"));
  try {
    const fixture = await createLauncherFixture(root);
    const data=join(root,"data"), trace=join(root,"trace");
    await seedRuntime(data,"0.30.1");
    await writeFile(join(data,"coconet/current/bin/coconet"), "#!/bin/sh\nexit 2\n");
    const result=await run(fixture.launcher,["update"],{env:launcherEnvironment(data,trace,"install")});
    assert.equal(result.code,0,result.stderr); assert.equal(result.stdout,"");
    assert.equal(await readFile(trace,"utf8"),"setup\n");
  } finally { await rm(root,{recursive:true,force:true}); }
});

test("terminating only the launcher cancels the upgrade and never runs the original command", {timeout:15000}, async () => {
  for (const signal of ["SIGINT", "SIGTERM"]) {
    const root=await mkdtemp(join(tmpdir(),"coconet-upgrade-cancel-"));
    try {
      const fixture=await createDownloadFixture(root,"wait-bootstrap");
      await seedRuntime(join(root,"data"),"0.30.1");
      let cancelled=false;
      const result=await run(fixture.launcher,["mcp"],{env:fixture.env,onStderr(chunk,child){
        if(!cancelled && chunk.includes("fixture bootstrap waiting")){cancelled=true;child.kill(signal);}
      }});
      assert.equal(cancelled,true); assert.equal(result.code,signal==="SIGINT"?130:143,result.stderr);
      assert.equal(result.stdout,"");
      const pid=Number(await readFile(join(root,"bootstrap-pid"),"utf8"));
      assert.throws(()=>process.kill(pid,0),{code:"ESRCH"});
      assert.deepEqual(await readdir(fixture.temporary),[]);
      assert.equal(JSON.parse(await readFile(join(root,"data/coconet/current/manifest.json"))).version,"0.30.1");
    } finally {await rm(root,{recursive:true,force:true});}
  }
});

async function createDownloadFixture(root, mode) {
  const fixture = await createLauncherFixture(root);
  for (const file of ["cli.mjs", "core.mjs", "progress.mjs"]) {
    await copyFile(join(packageRoot, file), join(fixture.root, file));
  }
  await symlink(join(packageRoot, "node_modules"), join(fixture.root, "node_modules"));
  const name = bundleName(fixture.version, resolveTarget());
  const bundleRoot = join(root, name);
  await mkdir(join(bundleRoot, "bin"), { recursive: true });
  await chmod(bundleRoot, 0o700);
  await chmod(join(bundleRoot, "bin"), 0o755);
  await writeFile(join(bundleRoot, "manifest.json"), "{}\n", { mode: 0o644 });
  await chmod(join(bundleRoot, "manifest.json"), 0o644);
  await writeFile(join(bundleRoot, "bin/coconet-bootstrap"), `#!/usr/bin/env node
const { mkdirSync, writeFileSync } = require("node:fs");
const { join } = require("node:path");
if (process.env.COCONET_TEST_DOWNLOAD_MODE === "wait-bootstrap") {
  writeFileSync(process.env.COCONET_TEST_BOOTSTRAP_PID, String(process.pid));
  process.stderr.write("fixture bootstrap waiting\\n");
  setInterval(() => {}, 1000);
} else {
  const current = join(process.env.XDG_DATA_HOME, "coconet/current");
  mkdirSync(join(current, "bin"), { recursive: true });
  writeFileSync(join(current, "manifest.json"), JSON.stringify({ version: "7.8.9" }, null, 2));
  writeFileSync(join(current, "bin/coconet"), "#!/bin/sh\\nprintf '%s\\\\n' '{\\"status\\":\\"ok\\"}'\\n", { mode: 0o755 });
  process.stdout.write("fixture installed\\n");
}
`, { mode: 0o755 });
  // Archive permissions are protocol inputs, independent of the test umask.
  await chmod(join(bundleRoot, "bin/coconet-bootstrap"), 0o755);
  const archive = join(root, "fixture.tar.gz");
  await tar.c({ cwd: root, file: archive, gzip: true }, [name]);
  const hash = createHash("sha256").update(await readFile(archive)).digest("hex");
  await writeFile(join(fixture.root, "release-manifest.json"), JSON.stringify({
    schema_version: 2, version: fixture.version, repository: "xfey/coconet", release_tag: `v${fixture.version}`,
    archives: { [`${name}.tar.gz`]: mode === "checksum-error" ? "0".repeat(64) : hash },
  }));
  const preload = join(root, "download.cjs");
  await writeFile(preload, `const { readFileSync } = require("node:fs");
const { setTimeout } = require("node:timers/promises");
process.stderr.isTTY = true;
process.stderr.columns = 140;
globalThis.fetch = async (url, { signal }) => {
  signal.throwIfAborted();
  const mode = process.env.COCONET_TEST_DOWNLOAD_MODE;
  if (mode === "wait-download") {
    await setTimeout(10000, undefined, { signal });
  }
  const data = readFileSync(process.env.COCONET_TEST_ARCHIVE);
  let offset = 0;
  const body = new ReadableStream({ async pull(controller) {
    await setTimeout(120, undefined, { signal });
    if (offset === data.length) { controller.close(); return; }
    const next = Math.min(offset + Math.ceil(data.length / 3), data.length);
    controller.enqueue(data.subarray(offset, next)); offset = next;
  }});
  const response = new Response(mode === "http-error" ? "unavailable" : body, {
    status: mode === "http-error" ? 503 : 200,
    headers: mode === "unknown-size" ? {} : { "content-length": String(data.length) },
  });
  Object.defineProperty(response, "url", { value: String(url) });
  return response;
};
`);
  const temporary = join(root, "temporary");
  await mkdir(temporary);
  return { ...fixture, temporary, env: {
    ...launcherEnvironment(join(root, "data"), join(root, "trace"), "install"),
    NODE_OPTIONS: `--require ${JSON.stringify(preload)}`, TERM: "xterm", TMPDIR: temporary,
    COCONET_TEST_ARCHIVE: archive, COCONET_TEST_DOWNLOAD_MODE: mode,
    COCONET_TEST_BOOTSTRAP_PID: join(root, "bootstrap-pid"),
  } };
}

async function createLauncherFixture(temporaryRoot, version = "7.8.9") {
  const root = join(temporaryRoot, `fixture-${Math.random().toString(16).slice(2)}`);
  const bin = join(root, "bin");
  await mkdir(bin, { recursive: true, mode: 0o755 });
  await writeFile(join(root, "package.json"), `${JSON.stringify({
    name: "coconet",
    version,
    type: "module",
    bin: { coconet: "./bin/coconet" },
  }, null, 2)}\n`, { mode: 0o644 });
  const launcher = join(bin, "coconet");
  await copyFile(launcherPath, launcher);
  for (const file of ["core.mjs", "compare-version.mjs"]) {
    await copyFile(join(packageRoot, file), join(root, file));
  }
  await chmod(launcher, 0o755);
  const runtimeSource = "#!/bin/sh\nprintf 'runtime'; for argument do printf ' <%s>' \"$argument\"; done; printf '\\n'\n";
  await writeFile(join(root, "cli.mjs"), `#!/usr/bin/env node
import { appendFile, chmod, mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
await appendFile(process.env.COCONET_TEST_TRACE, "setup\\n");
process.stdout.write("fixture setup output\\n");
const mode = process.env.COCONET_TEST_SETUP;
if (mode === "fail") process.exit(42);
if (mode === "incomplete") process.exit(0);
const current = join(process.env.XDG_DATA_HOME, "coconet", "current");
await mkdir(join(current, "bin"), { recursive: true });
await writeFile(join(current, "manifest.json"), JSON.stringify({ version: ${JSON.stringify(version)} }, null, 2) + "\\n");
const runtime = join(current, "bin", "coconet");
await writeFile(runtime, ${JSON.stringify(runtimeSource)});
await chmod(runtime, 0o755);
`, { mode: 0o755 });
  await chmod(join(root, "cli.mjs"), 0o755);
  return { root, launcher, version };
}

async function seedRuntime(dataRoot, version) {
  const current = join(dataRoot, "coconet", "current");
  await mkdir(join(current, "bin"), { recursive: true });
  await writeFile(join(current, "manifest.json"), `${JSON.stringify({ version }, null, 2)}\n`);
  const runtime = join(current, "bin", "coconet");
  await writeFile(
    runtime,
    "#!/bin/sh\nprintf 'runtime'; for argument do printf ' <%s>' \"$argument\"; done; printf '\\n'\n",
    { mode: 0o755 },
  );
  await chmod(runtime, 0o755);
}

function launcherEnvironment(dataRoot, trace, setupMode, npmCache = "") {
  return {
    ...process.env,
    COCONET_TEST_SETUP: setupMode,
    COCONET_TEST_TRACE: trace,
    XDG_DATA_HOME: dataRoot,
    npm_config_audit: "false",
    npm_config_cache: npmCache || join(dataRoot, "npm-cache"),
    npm_config_fund: "false",
    npm_config_update_notifier: "false",
  };
}

function run(command, arguments_, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, arguments_, {
      cwd: options.cwd,
      env: options.env || process.env,
      stdio: [options.stdin === undefined ? "ignore" : "pipe", "pipe", "pipe"],
    });
    if (options.stdin !== undefined) child.stdin.end(options.stdin);
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
      options.onStderr?.(chunk, child);
    });
    child.once("error", reject);
    child.once("close", (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}
