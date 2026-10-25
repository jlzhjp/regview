// Reports non-canonical Tailwind classes in templ, TypeScript strings,
// and CSS @apply directives. Tailwind's canonicalizeCandidates is
// marked unstable; keep the tailwindcss version pinned and revisit after
// upgrades.
import { __unstable__loadDesignSystem } from '@tailwindcss/node';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

type Finding = {
  file: string;
  line: number;
  column: number;
  message: string;
};

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const entryPoint = path.join(root, 'frontend/src/app.css');
const rootFontSize = 16;

const design = await __unstable__loadDesignSystem(fs.readFileSync(entryPoint, 'utf8'), {
  base: path.dirname(entryPoint),
});

function walk(directory: string, extension: string, files: string[] = []): string[] {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      if (['node_modules', 'dist', 'generated'].includes(entry.name)) continue;
      walk(full, extension, files);
    } else if (entry.name.endsWith(extension)) {
      files.push(full);
    }
  }
  return files;
}

const sources = [
  ...walk(path.join(root, 'internal'), '.templ'),
  ...walk(path.join(root, 'frontend/src'), '.ts'),
  ...walk(path.join(root, 'frontend/src'), '.css'),
];

function location(source: string, offset: number): { line: number; column: number } {
  const line = source.slice(0, offset).split('\n').length;
  return { line, column: offset - source.lastIndexOf('\n', offset - 1) };
}

function canonical(token: string): string | undefined {
  const [result] = design.canonicalizeCandidates([token], { rem: rootFontSize });
  return result && result !== token ? result : undefined;
}

function tokens(value: string): { token: string; offset: number }[] {
  return [...value.matchAll(/\S+/g)].map((match) => ({
    token: match[0],
    offset: match.index ?? 0,
  }));
}

const findings: Finding[] = [];
const seen = new Set<string>();

function report(file: string, source: string, offset: number, token: string): void {
  const key = `${file}:${offset}:${token}`;
  if (seen.has(key)) return;
  seen.add(key);
  const replacement = canonical(token);
  if (!replacement) return;
  const { line, column } = location(source, offset);
  findings.push({
    file: path.relative(root, file),
    line,
    column,
    message: `The class "${token}" can be written as "${replacement}".`,
  });
}

for (const file of sources) {
  const source = fs.readFileSync(file, 'utf8');

  if (file.endsWith('.templ') || file.endsWith('.ts')) {
    // Static class attributes. Dynamic values with templ/JS expressions are
    // skipped because they cannot be canonicalized without evaluating them.
    for (const match of source.matchAll(/class\s*=\s*(["'])([\s\S]*?)\1/g)) {
      const value = match[2];
      if (/[{}$]/.test(value)) continue;
      const valueStart = (match.index ?? 0) + match[0].indexOf(match[1]) + 1;
      for (const { token, offset } of tokens(value)) {
        report(file, source, valueStart + offset, token);
      }
    }
  }

  if (file.endsWith('.ts')) {
    // Class constants such as BASE_CLASSES. Limit generic strings to tokens
    // with arbitrary values so prose and unrelated strings stay untouched.
    for (const match of source.matchAll(/(["'`])((?:\\.|(?!\1)[\s\S])*?)\1/g)) {
      const value = match[2];
      if (value.includes('${')) continue;
      const valueStart = (match.index ?? 0) + 1;
      for (const { token, offset } of tokens(value)) {
        if (token.includes('[')) report(file, source, valueStart + offset, token);
      }
    }
  }

  if (file.endsWith('.css')) {
    for (const match of source.matchAll(/@apply\s+([^;]+);/g)) {
      const value = match[1];
      const valueStart = (match.index ?? 0) + match[0].indexOf(value);
      for (const { token, offset } of tokens(value)) {
        report(file, source, valueStart + offset, token);
      }
    }
  }
}

for (const finding of findings) {
  console.log(`${finding.file}:${finding.line}:${finding.column}: ${finding.message}`);
}
if (findings.length) {
  console.log(
    `\n${findings.length} non-canonical Tailwind class${findings.length === 1 ? '' : 'es'}.`,
  );
  process.exitCode = 1;
}
