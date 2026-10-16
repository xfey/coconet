import assert from "node:assert/strict";
import { setTimeout } from "node:timers/promises";
import test from "node:test";
import { SetupProgress } from "./progress.mjs";

test("terminal feedback animates, shows transfer size, and stops cleanly", async () => {
  const chunks = [];
  const progress = new SetupProgress({ isTTY: true, columns: 120, write: (text) => chunks.push(text) }, {});
  try {
    progress.stage("Downloading Coconet...");
    progress.download(1024 * 1024, 2 * 1024 * 1024);
    await setTimeout(220);
    assert.ok(chunks.length >= 2);
    assert.match(chunks.join(""), /1\.0 MB \/ 2\.0 MB, 50%/u);
    assert.match(chunks[0], /\| Downloading/u);
    assert.match(chunks[1], /\/ Downloading/u);
    progress.stop();
    const stopped = chunks.length;
    assert.equal(chunks.at(-1), "\r\x1b[2K");
    await setTimeout(120);
    assert.equal(chunks.length, stopped);
  } finally {
    progress.stop();
  }
});

test("redirected and dumb terminals produce only bounded phase lines", () => {
  for (const [isTTY, environment] of [[false, {}], [true, { TERM: "dumb" }]]) {
    let output = "";
    const progress = new SetupProgress({ isTTY, write: (text) => { output += text; } }, environment);
    progress.stage("Downloading...");
    for (let i = 0; i < 1000; i += 1) progress.download(i, 0);
    progress.stage("Verifying...");
    progress.stop();
    assert.equal(output, "coconet: Downloading...\ncoconet: Verifying...\n");
  }
});

test("unknown transfer sizes remain honest and narrow terminals do not wrap", () => {
  let output = "";
  const terminal = { isTTY: true, columns: 100, write: (text) => { output += text; } };
  const progress = new SetupProgress(terminal, {});
  try {
    progress.stage("Downloading...");
    for (const total of [0, NaN, Infinity, -1, 1]) {
      progress.download(1024 * 1024, total);
      progress.render();
    }
    assert.match(output, /1\.0 MB downloaded/u);
    assert.doesNotMatch(output, /%|NaN|Infinity/u);
    terminal.columns = 12;
    output = "";
    progress.stage("Installing and configuring Agent integrations...");
    assert.ok(output.split("\x1b[2K").at(-1).length < terminal.columns);
  } finally {
    progress.stop();
  }
});
