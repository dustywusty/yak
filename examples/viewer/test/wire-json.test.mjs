import { test } from 'node:test';
import assert from 'node:assert/strict';
export { parseWireJSON, stringifyWireJSON, compareRevision } from '../static/wire-json.js';
import { parseWireJSON, stringifyWireJSON, compareRevision } from '../static/wire-json.js';

for (const revision of ['5', '9007199254740993', '9223372036854775807']) {
  test(`OP17-043 nested revision ${revision} retains decimal source`, () => {
    const doc = parseWireJSON(`{"tasks":[{"revision":${revision},"brief_revision":${revision}}],"thread":{"revision":${revision},"messages":[{"revision":${revision}}]},"payload":{"task":{"revision":${revision}}},"count":5}`);
    assert.equal(doc.tasks[0].revision, revision);
    assert.equal(doc.tasks[0].brief_revision, revision);
    assert.equal(doc.thread.revision, revision);
    assert.equal(doc.thread.messages[0].revision, revision);
    assert.equal(doc.payload.task.revision, revision);
    assert.equal(doc.count, 5);
  });
}
test('OP17-043 numeric ordering spans digit lengths and adjacent unsafe integers', () => {
  const revisions = ['9', '10', '9007199254740992', '9007199254740993', '9223372036854775807'];
  for (let i = 0; i < revisions.length; i++) for (let j = 0; j < revisions.length; j++) {
    assert.equal(compareRevision(revisions[i], revisions[j]), Math.sign(i - j));
  }
  assert.equal(compareRevision('5', 5), 0);
});
test('OP17-043 tokenizer respects escaped strings keys arrays and nesting', () => {
  const text = 'literal "revision":9007199254740993, braces { [ and slash \\';
  const raw = JSON.stringify({ text, labels: { revision: 'ordinary user string' }, list: [5, { text }] });
  assert.deepEqual(parseWireJSON(raw), JSON.parse(raw));
  assert.equal(parseWireJSON('{"revi\\u0073ion":9007199254740993}').revision, '9007199254740993');
  assert.equal(parseWireJSON('{"revision":1,"revision":10}').revision, '10');
});
for (const token of ['-1', '-0', '0', '1.5', '1e2', '9223372036854775808', '01', 'NaN', 'null', 'true', '{}', '[]']) {
  test(`OP17-043 rejects invalid revision token ${token}`, () => {
    for (const key of ['revision', 'brief_revision']) assert.throws(() => parseWireJSON(`{"${key}":${token}}`));
  });
}
test('OP17-043 JSON structure remains strictly validated', () => {
  for (const raw of ['{"revision":1,}', '{"revision":1} trailing', '{"revision" 1}', '[1,]', '{"x":"unterminated}', '{"x":undefined}']) assert.throws(() => parseWireJSON(raw));
});
test('OP17-043 claim expectation serializes exact decimal as a JSON number only at schema field', () => {
  for (const revision of ['5', '9007199254740993', '9223372036854775807']) {
    const value = { expect_brief_revision: revision, lease_seconds: 300, text: revision, labels: { expect_brief_revision: revision, revision } };
    const raw = stringifyWireJSON(value);
    assert.ok(raw.includes(`"expect_brief_revision":${revision},`), raw);
    assert.equal(JSON.parse(raw).text, revision);
    assert.deepEqual(JSON.parse(raw).labels, value.labels);
    assert.equal(value.expect_brief_revision, revision);
  }
});
test('OP17-043 invalid outgoing expectations and unsafe comparison inputs fail closed', () => {
  for (const value of ['-1', '0', '01', '1e2', '1.5', '9223372036854775808', '1,"injected":true', 9007199254740992, 1n, null]) {
    assert.throws(() => stringifyWireJSON({ expect_brief_revision: value }), String(value));
    assert.throws(() => compareRevision(value, '5'), String(value));
  }
  assert.equal(stringifyWireJSON({ expect_brief_revision: 5 }), '{"expect_brief_revision":5}');
  assert.equal(stringifyWireJSON({ text: '"expect_brief_revision":"9007199254740993"' }), JSON.stringify({ text: '"expect_brief_revision":"9007199254740993"' }));
});
