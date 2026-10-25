import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

interface PackageLicense {
  name: string;
  license: string;
  paths: (string | null)[];
}

const output = process.argv[2];
if (!output) throw new Error('Usage: node scripts/collect-npm-licenses.ts <output-directory>');

// Collect production dependencies, including their transitive dependencies.
const report: Record<string, PackageLicense[]> = JSON.parse(
  execFileSync('pnpm', ['licenses', 'list', '--prod', '--json'], { encoding: 'utf8' }),
);
const packages = new Map<
  string,
  { name: string; version: string; license: string; files: string[] }
>();

for (const entry of Object.values(report).flat()) {
  for (const path of entry.paths) {
    if (!path) throw new Error(`No installed path for ${entry.name}`);
    const { name, version }: { name: string; version: string } = JSON.parse(
      readFileSync(join(path, 'package.json'), 'utf8'),
    );
    const id = `${name}@${version}`;
    const files = readdirSync(path, { withFileTypes: true })
      .filter((file) => file.isFile() && /^(licen[sc]e|copying|notice)([._-].*)?$/i.test(file.name))
      .map((file) => file.name)
      .sort();
    mkdirSync(join(output, id), { recursive: true });
    for (const file of files) copyFileSync(join(path, file), join(output, id, file));
    if (files.length === 0) {
      console.warn(
        `No license file shipped by ${id}; recording declared license ${entry.license}.`,
      );
    }
    packages.set(id, { name, version, license: entry.license, files });
  }
}

// Do not publish pnpm's machine-specific package paths.
mkdirSync(output, { recursive: true });
writeFileSync(
  join(output, 'index.json'),
  JSON.stringify(
    [...packages.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([, entry]) => entry),
    null,
    2,
  ) + '\n',
);
console.log(`Collected npm license notices for ${packages.size} installed packages.`);
