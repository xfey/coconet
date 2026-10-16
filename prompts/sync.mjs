import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const promptsRoot = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.dirname(promptsRoot);

const skill = await readFile(path.join(promptsRoot, "coconet-team-context.SKILL.md"));
for (const target of [
  "plugins/codex/coconet/skills/coconet-team-context/SKILL.md",
  "plugins/claude/coconet/skills/coconet-team-context/SKILL.md",
  "plugins/cursor/coconet/skills/coconet-team-context/SKILL.md",
]) {
  await writeFile(path.join(repositoryRoot, target), skill);
}

const defaultPrompt = (await readFile(path.join(promptsRoot, "codex-default-prompt.txt"), "utf8")).trim();
const manifestPath = path.join(repositoryRoot, "plugins/codex/coconet/.codex-plugin/plugin.json");
const manifestSource = await readFile(manifestPath, "utf8");
const manifest = JSON.parse(manifestSource);
if (!Array.isArray(manifest.interface?.defaultPrompt)) {
  throw new Error("Codex Plugin manifest has no valid defaultPrompt array");
}
manifest.interface.defaultPrompt = [defaultPrompt];
await writeFile(manifestPath, JSON.stringify(manifest, null, 2) + "\n");
