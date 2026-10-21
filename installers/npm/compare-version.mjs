import { compareVersions } from './core.mjs';

// Called only when the manifest versions differ; normal launches stay native.
process.stdout.write(String(compareVersions(process.argv[2], process.argv[3])));
