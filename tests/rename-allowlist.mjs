import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

const oldName = `cod${"dis"}`;
const oldNamePattern = new RegExp(oldName, "i");

// These are immutable validation records or frozen legacy fixtures. Listing
// each file explicitly prevents a newly added file in the same directory from
// inheriting an old-name exemption.
const historicalFiles = new Set([
  "apps/release/README.md",
  "deploy/README.md",
  "deploy/hosted/systemd/coconet-server-continuity.service",
  "deploy/sync-server-release.sh",
  "docs/archive/plans/coconet-renaming-roadmap.md",
  "docs/validation/capacity-readiness/release.md",
  "docs/decisions/0024-public-github-release-and-thin-npm-installer.md",
  "docs/archive/product/mvp-validation.md",
  "docs/archive/plans/project-connection-refactor-roadmap.md",
  "docs/roadmap.md",
  "docs/validation/phase-0/README.md",
  "docs/validation/phase-1/README.md",
  "docs/validation/phase-2/README.md",
  "docs/validation/phase-5/README.md",
  "docs/validation/phase-7/README.md",
  "docs/validation/coconet-renaming/README.md",
  "docs/validation/project-connection-refactor/README.md",
  "docs/validation/public-release/README.md",
  "docs/validation/recent-session-discovery/README.md",
  "tests/integration/README.md",
]);

const legacyDeclarationFiles = new Set([
  "docs/decisions/0017-one-project-per-logical-repository.md",
  "docs/decisions/0026-lightweight-project-connections.md",
]);

const wireHistoryFiles = new Map([
  [
    "apps/server/store_migration_test.go",
    [new RegExp(`application/x-${oldName}-codex-jsonl`, "i")],
  ],
]);

const historicalLinePatterns = new Map([
  // Hosted retains its original OS account and deployment directory; these
  // exact files document that infrastructure, not a public product identity.
  ["deploy/STORAGE.md", [new RegExp(`/var/lib/${oldName}`)]],
  ["deploy/hosted/backup.json", [new RegExp(`/var/lib/${oldName}/`)]],
  ["deploy/hosted/storage-check.json", [new RegExp(`/var/lib/${oldName}`)]],
  ["deploy/hosted/systemd/coconet-backup.service", [
    new RegExp(`^(User|Group)=${oldName}$`),
    new RegExp(`^ReadWritePaths=/var/lib/${oldName}/backups$`),
  ]],
  [
    "docs/validation/prompt-pipeline-input/release.md",
    [new RegExp(`systemd 使用原 \u0060${oldName}\u0060 用户`)],
  ],
  [
    "docs/validation/workspace-registry/README.md",
    [new RegExp(`/var/backups/${oldName}/coconet-upgrade-0\\.16\\.0-20260915T094352Z`)],
  ],
  [
    "apps/server/README.md",
    [new RegExp(`历史 ${oldName}.*0\\.1\\.1`, "i")],
  ],
  [
    "docs/archive/plans/agent-driven-session-recall-roadmap.md",
    [
      new RegExp(`发布身份.*以 ${oldName} 身份.*0\\.3\\.0`, "i"),
      new RegExp(`发布 ${oldName}.*0\\.3\\.0`, "i"),
    ],
  ],
  [
    "docs/decisions/0025-developer-id-signing-and-explicit-notarization.md",
    [
      new RegExp(`历史 ${oldName}.*0\\.1\\.0.*0\\.3\\.0`, "i"),
      new RegExp(`属于 ${oldName} identifier`, "i"),
    ],
  ],
  [
    "docs/archive/product/installation-and-runtime.md",
    [
      new RegExp(`xfey/${oldName}.*npm .*${oldName}.*v0\\.3\\.0`, "i"),
      new RegExp(`${oldName}.*0\\.2\\.1.*0\\.3\\.0`, "i"),
    ],
  ],
]);

const infrastructureTokens = [
  new RegExp(`api\\.${oldName}\\.work`, "gi"),
  new RegExp(`schemas\\.${oldName}\\.dev`, "gi"),
  new RegExp(`releases\\.${oldName}\\.work`, "gi"),
];

const trackedFiles = execFileSync(
  "git",
  ["ls-files", "--cached", "--others", "--exclude-standard", "-z"],
  {
    encoding: "utf8",
  },
).split("\0").filter(Boolean);

const allowedOldNamePaths = new Set([
  `deploy/hosted/nginx/api.${oldName}.work.bootstrap.conf`,
  `deploy/hosted/nginx/api.${oldName}.work.conf`,
]);

const violations = [];
for (const file of trackedFiles) {
  if (oldNamePattern.test(file) && !allowedOldNamePaths.has(file)) {
    violations.push(`${file}: path contains the retired product name`);
  }

  let content;
  try {
    content = readFileSync(file, "utf8");
  } catch {
    continue;
  }

  for (const [index, originalLine] of content.split("\n").entries()) {
    if (!oldNamePattern.test(originalLine)) continue;
    if (historicalFiles.has(file)) continue;
    if (historicalLinePatterns.get(file)?.some((pattern) => pattern.test(originalLine))) {
      continue;
    }
    if (wireHistoryFiles.get(file)?.some((pattern) => pattern.test(originalLine))) {
      continue;
    }

    let line = originalLine;
    for (const token of infrastructureTokens) line = line.replace(token, "");
    if (legacyDeclarationFiles.has(file)) {
      line = line
        .replace(new RegExp(`\\.${oldName}/project\\.json`, "gi"), "")
        .replace(new RegExp(`${oldName} manage`, "gi"), "");
    }
    if (file === "docs/README.md") {
      line = line.replace(new RegExp(`${oldName}\\s*→\\s*Coconet`, "gi"), "");
    }

    if (oldNamePattern.test(line)) {
      violations.push(`${file}:${index + 1}: ${originalLine.trim()}`);
    }
  }
}

if (violations.length > 0) {
  process.stderr.write("Retired product-name references escaped the exact allowlist:\n");
  process.stderr.write(`${violations.join("\n")}\n`);
  process.exit(1);
}

process.stdout.write("Coconet rename allowlist check passed.\n");
