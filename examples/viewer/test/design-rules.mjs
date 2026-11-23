import { readdir, readFile } from 'node:fs/promises';
import { join } from 'node:path';

function violations(file, source) {
  const text = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  const issues = [];
  if (file.endsWith('.css')) {
    if (/(?:linear|radial|conic)-gradient\s*\(/i.test(text)) issues.push('gradient');
    if (/backdrop-filter\s*:/i.test(text)) issues.push('backdrop filter');
    if (/@(?:-webkit-)?keyframes\b/i.test(text)) issues.push('animation');
    for (const [, value] of text.matchAll(/border-(?:left|top)(?:-width)?\s*:\s*([^;}]+)/gi)) {
      if (Number(value.match(/([\d.]+)px/i)?.[1]) >= 3) issues.push('stripe');
    }
    for (const [block] of text.matchAll(/\{[^{}]*\}/g)) {
      if (/text-transform\s*:\s*uppercase/i.test(block) && /letter-spacing\s*:/i.test(block)) issues.push('eyebrow');
    }
  }
  // JS strings supply UI copy. API values remain data at render time.
  const copy = file.endsWith('.js')
    ? (text.match(/"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`/g) || []).join(' ')
    : text.replace(/<!--[\s\S]*?-->/g, '');
  if (/\p{Emoji_Presentation}|\uFE0F/u.test(copy)) issues.push('emoji');
  if (copy.includes('—')) issues.push('em dash');
  return issues;
}

export async function checkDesignRules(directory) {
  for (const file of await readdir(directory)) {
    if (!/\.(?:js|css|html)$/.test(file)) continue;
    const issues = violations(file, await readFile(join(directory, file), 'utf8'));
    if (issues.length) throw new Error(file + ': ' + issues.join(', '));
  }
}
