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

// Raw tokens deliberately include exponent notation and unsafe protocol integers.
export const int01DataCases = [];
for (const key of ['revision', 'brief_revision']) {
  for (const token of ['0', 'null', '1.5', '-1', '[]', '[0,{"revision":0,"brief_revision":null}]', '{}', '{"custom":true}', '"user"', 'true', 'false', '5', '1e2', '1.25']) {
    int01DataCases.push({ name: `${key}=${token}`, raw: `{"${key}":${token}}` });
  }
}
int01DataCases.push(
  { name: 'reported combined object', raw: '{"revision":0,"brief_revision":null,"nested":{"revision":{"custom":true}}}' },
  { name: 'escaped names and fake protocol wrappers', raw: '{"revi\\u0073ion":5,"nested":[{"task":{"revision":0,"outcome":{"data":{"brief_revision":null}}},"claim":{"revision":1.25}},[{"brief_revision":false}]]}' }
);
export const int01Task = data => `{"id":"t","revision":9007199254740993,"brief_revision":9223372036854775807,"outcome":{"data":${data}}}`;

for (const { name, raw } of int01DataCases) {
  test(`OP17-043 INT-01 opaque matrix ${name}`, () => {
    const expected = JSON.parse(raw);
    const envelopes = [
      [`{"outcome":{"data":${raw}}}`, d => d.outcome.data],
      [`{"payload":{"previous_outcome":{"data":${raw}}}}`, d => d.payload.previous_outcome.data],
      [`{"extensions":{"vendor.feature":${raw}}}`, d => d.extensions['vendor.feature']],
      [`{"metadata":${raw}}`, d => d.metadata],
      [`{"audit_events":[{"metadata":${raw}}]}`, d => d.audit_events[0].metadata]
    ];
    for (const [text, data] of envelopes) assert.deepEqual(data(parseWireJSON(text)), expected);
  });
}
test('OP17-043 INT-01 positive user number retains number type', () => {
  const data = parseWireJSON('{"outcome":{"data":{"revision":5}}}').outcome.data;
  assert.equal(typeof data.revision, 'number');
  assert.equal(data.revision, 5);
});
test('OP17-043 INT-01 escaped boundaries null and strict protocol barriers', () => {
  const doc = parseWireJSON('{"outc\\u006fme":{"da\\u0074a":{"revision":0}},"extens\\u0069ons":{"vendor.feature":{"revision":null}},"metad\\u0061ta":{"brief_revision":false},"labels":{"revision":"user","brief_revision":"text"}}');
  assert.deepEqual(doc.outcome.data, { revision: 0 });
  assert.deepEqual(doc.extensions, { 'vendor.feature': { revision: null } });
  assert.deepEqual(doc.metadata, { brief_revision: false });
  assert.deepEqual(doc.labels, { revision: 'user', brief_revision: 'text' });
  assert.equal(parseWireJSON('{"outcome":{"data":null}}').outcome.data, null);
  for (const raw of ['{"outcome":{"data":{"revision":0}},"revision":0}', '{"revision":0,"outcome":{"data":{"revision":0}}}', '{"outcome":{"data":{"x":undefined}}}', '{"metadata":[1,]}', '{"extensions":{"vendor.feature":{"revision":01}}}']) assert.throws(() => parseWireJSON(raw));
});
test('OP17-043 INT-01 typed snapshot event and claim wrappers stay exact', () => {
  const revision = '9007199254740993', max = '9223372036854775807';
  const doc = parseWireJSON(`{"items":[{"type":"task","data":${int01Task('null')}},{"type":"thread","data":{"revision":${revision}}},{"type":"message","data":{"revision":${max},"extensions":{"vendor.feature":{"revision":0}}}}],"payload":{"task":${int01Task('{}')},"claim":{"revision":${max},"brief_revision":${revision}},"message":{"revision":${max}},"thread":{"revision":${revision}},"brief_revision":${max}},"resource":{"revision":${revision}},"brief":{"revision":${max}},"outcome":"claimed"}`);
  assert.deepEqual(doc.items.map(i => i.data.revision), [revision, revision, max]);
  assert.equal(doc.items[0].data.brief_revision, max);
  assert.equal(doc.payload.task.revision, revision);
  assert.equal(doc.payload.claim.revision, max);
  assert.equal(doc.payload.claim.brief_revision, revision);
  assert.equal(doc.payload.message.revision, max);
  assert.equal(doc.payload.thread.revision, revision);
  assert.equal(doc.payload.brief_revision, max);
  assert.equal(doc.resource.revision, revision);
  assert.equal(doc.brief.revision, max);
});

for (const boundary of ['metadata', 'extensions', 'outcome', 'previous_outcome']) {
  test(`OP17-043 INT-01 B1 unknown wrappers ${boundary} preserve opaque matrix`, () => {
    for (const { raw } of int01DataCases) {
      const member = boundary === 'outcome' || boundary === 'previous_outcome'
        ? `"${boundary}":{"data":${raw}}` : `"${boundary}":${raw}`;
      for (const text of [`{"unknown":{${member}}}`, `{"unknown":[{"more":[[{${member}}]]}]}`]) {
        assert.deepEqual(parseWireJSON(text), JSON.parse(text));
      }
    }
    // Preserve the exact parent-approved namespaced extension counterexample.
    const text = '{"unknown":{"extensions":{"vendor.feature":{"brief_revision":null,"revision":5}}}}';
    assert.deepEqual(parseWireJSON(text), JSON.parse(text));
  });
}
test('OP17-043 INT-01 B2 unrelated unknown and near-miss keys retain behavior', () => {
  for (const raw of ['{"unknown":{"revision":0}}', '{"unknown":{"brief_revision":null}}']) assert.throws(() => parseWireJSON(raw), RangeError);
  const unknown = parseWireJSON('{"unknown":{"revision":9007199254740993,"brief_revision":9223372036854775807,"text":{"revision":"user text"}}}').unknown;
  assert.equal(unknown.revision, '9007199254740993');
  assert.equal(unknown.brief_revision, '9223372036854775807');
  assert.equal(unknown.text.revision, 'user text');
  for (const key of ['Metadata', 'metadata_extra', 'extension', 'outcomes']) {
    assert.throws(() => parseWireJSON(`{"${key}":{"data":{"revision":0}}}`), RangeError);
    assert.equal(parseWireJSON(`{"${key}":{"revision":9007199254740993}}`)[key].revision, '9007199254740993');
  }
});
test('OP17-043 INT-01 B3 snapshot data and event payload remain traversed', () => {
  const doc = parseWireJSON('{"items":[{"type":"task","data":{"revision":9007199254740993,"brief_revision":9223372036854775807,"outcome":{"data":{"revision":0}}}}],"payload":{"task":{"revision":9007199254740993},"claim":{"brief_revision":9223372036854775807}},"resource":{"revision":9007199254740993}}');
  assert.equal(doc.items[0].data.revision, '9007199254740993');
  assert.equal(doc.items[0].data.brief_revision, '9223372036854775807');
  assert.deepEqual(doc.items[0].data.outcome.data, { revision: 0 });
  assert.equal(doc.payload.task.revision, '9007199254740993');
  assert.equal(doc.payload.claim.brief_revision, '9223372036854775807');
  assert.equal(doc.resource.revision, '9007199254740993');
});
test('OP17-043 INT-01 B4 outcome own revisions remain typed', () => {
  for (const key of ['outcome', 'previous_outcome']) {
    const doc = parseWireJSON(`{"unknown":{"${key}":{"revision":9007199254740993,"data":{"revision":0}}}}`).unknown[key];
    assert.equal(doc.revision, '9007199254740993');
    assert.deepEqual(doc.data, { revision: 0 });
    assert.throws(() => parseWireJSON(`{"${key}":{"revision":0,"data":{"revision":0}}}`), RangeError);
  }
  assert.throws(() => parseWireJSON('{"task":{"revision":0,"outcome":{"data":{"revision":0}}}}'), RangeError);
});
test('OP17-043 INT-01 B5 only direct outcome object data is opaque', () => {
  for (const raw of ['{"unknown":{"data":{"revision":0}}}', '{"outcome":{"wrapper":{"data":{"revision":0}}}}', '{"payload":{"data":{"revision":0}}}', '{"outcome":[{"data":{"revision":0}}]}', '{"previous_outcome":{"wrapper":{"data":{"revision":0}}}}']) assert.throws(() => parseWireJSON(raw), RangeError);
  assert.equal(parseWireJSON('{"data":{"revision":9007199254740993}}').data.revision, '9007199254740993');
});
test('OP17-043 INT-01 B6 escaped boundaries and member order preserve entire subtrees', () => {
  for (const { raw } of int01DataCases) {
    for (const outcome of ['out\\u0063ome', 'previous_out\\u0063ome']) {
      for (const members of [`"d\\u0061ta":${raw},"revi\\u0073ion":9007199254740993`, `"revi\\u0073ion":9007199254740993,"d\\u0061ta":${raw}`]) {
        const doc = parseWireJSON(`{"${outcome}":{${members}}}`)[JSON.parse(`"${outcome}"`)];
        assert.deepEqual(doc.data, JSON.parse(raw));
        assert.equal(doc.revision, '9007199254740993');
      }
    }
    for (const key of ['\\u006detadata', 'extens\\u0069ons']) {
      const text = `{"unknown":{"${key}":${raw}}}`;
      assert.deepEqual(parseWireJSON(text), JSON.parse(text));
    }
  }
});
test('OP17-043 INT-01 B7 opaque subtrees require valid JSON', () => {
  for (const raw of ['{"metadata":{"revision":0,}}', '{"outcome":{"data":{"revision":01}}}', '{"extensions":{"vendor.feature":[0,]}}', '{"metadata":{"bad":"\\q"}}', '{"metadata":{"revision":0}} trailing']) assert.throws(() => parseWireJSON(raw), SyntaxError);
});
