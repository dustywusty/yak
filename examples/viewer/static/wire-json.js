// Browser revisions are decimal strings. Never pass an int64 through Number.
const maxRevision = '9223372036854775807';
function decimalRevision(value) {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) throw new RangeError('Unsafe numeric revision');
    value = String(value);
  }
  if (typeof value !== 'string' || !/^[1-9][0-9]*$/.test(value) ||
      value.length > maxRevision.length || (value.length === maxRevision.length && value > maxRevision)) {
    throw new RangeError('Revision must be a positive int64 decimal');
  }
  return value;
}

// Walk JSON lexically so only whole property values can be transformed.
// Strings (including escaped property names), arrays and objects have their
// own boundaries. JSON.parse validates the final JSON, including primitives.
function transformJSON(text, transform) {
  let pos = 0;
  const edits = [], nodes = [];
  const space = () => { while (pos < text.length && /[\t\n\r ]/.test(text[pos])) pos++; };
  const take = (ch) => { space(); if (text[pos++] !== ch) throw new SyntaxError(`Expected ${ch}`); };
  const string = () => {
    const start = pos++;
    while (pos < text.length) {
      const ch = text[pos++];
      if (ch === '\\') pos++;
      else if (ch === '"') return JSON.parse(text.slice(start, pos));
    }
    throw new SyntaxError('Unterminated JSON string');
  };
  function value(key, depth, opaque = false, parent = null) {
    space();
    const start = pos;
    const node = { key, depth, opaque, parent, start, kind: text[pos], members: new Map() };
    if (text[pos] === '{') {
      pos++; space();
      if (text[pos] !== '}') for (;;) {
        space();
        if (text[pos] !== '"') throw new SyntaxError('Expected property name');
        const name = string(); take(':');
        // Structural exceptions for Message.extensions, AuditEvent.metadata,
        // and direct Outcome.data / previous_outcome.data, at any wrapper depth.
        // This is boundary recognition, not validation of the enclosing schema.
        const childOpaque = opaque || name === 'extensions' || name === 'metadata' ||
          (name === 'data' && (key === 'outcome' || key === 'previous_outcome'));
        node.members.set(name, value(name, depth + 1, childOpaque, node)); space();
        if (text[pos] !== ',') break;
        pos++;
      }
      take('}');
    } else if (text[pos] === '[') {
      pos++; space();
      if (text[pos] !== ']') for (;;) {
        value(undefined, depth + 1, opaque, node); space();
        if (text[pos] !== ',') break;
        pos++;
      }
      take(']');
    } else if (text[pos] === '"') string();
    else {
      while (pos < text.length && !/[\t\n\r ,}\]]/.test(text[pos])) pos++;
      if (pos === start) throw new SyntaxError('Expected JSON value');
    }
    node.raw = text.slice(start, pos); node.end = pos;
    nodes.push(node);
    return node;
  }
  value(undefined, 0); space();
  if (pos !== text.length) throw new SyntaxError('Trailing JSON data');
  // Defer conversion until complete envelopes are available, preserving raw tokens.
  for (const node of nodes) {
    const replacement = transform(node.key, node.raw, node.depth, node.opaque, node);
    if (replacement !== node.raw) edits.push({ start: node.start, end: node.end, replacement });
  }
  // Transforms only replace primitive revision values, so edits cannot overlap.
  let result = '', end = 0;
  for (const edit of edits) { result += text.slice(end, edit.start) + edit.replacement; end = edit.end; }
  return result + text.slice(end);
}

// These predicates recognize only the approved structural Event role. They do
// not validate payload variants, timestamps, known event kinds or cursor syntax.
const objectNode = node => node?.kind === '{';
const member = (node, key) => node?.members.get(key);
const stringNode = node => node?.kind === '"';
const nonemptyString = node => stringNode(node) && JSON.parse(node.raw).length > 0;
const nullableString = node => stringNode(node) || node?.raw === 'null';
function eventNode(node) {
  if (!objectNode(node)) return false;
  const get = key => member(node, key);
  const resource = get('resource');
  return nonemptyString(get('id')) && nonemptyString(get('cursor')) &&
    stringNode(get('type')) && /^[a-z_]+\.[a-z_]+$/.test(JSON.parse(get('type').raw)) &&
    ['board_id', 'actor_id', 'recorded_at', 'effective_at'].every(key => stringNode(get(key))) &&
    ['credential_id', 'run_id', 'channel_id', 'thread_id'].every(key => nullableString(get(key))) &&
    objectNode(get('payload')) && objectNode(resource) &&
    stringNode(member(resource, 'type')) && stringNode(member(resource, 'id')) &&
    resource.members.has('revision');
}
function nullableEventRevision(node) {
  const resource = node.parent, event = resource?.parent;
  if (node.key !== 'revision' || resource?.key !== 'resource' ||
      member(event, 'resource') !== resource || !eventNode(event)) return false;
  if (event.depth === 0) return true;
  const events = event.parent, page = events?.parent;
  return events?.kind === '[' && events.key === 'events' && page?.depth === 0 &&
    objectNode(page) && member(page, 'events') === events &&
    nonemptyString(member(page, 'next_cursor')) &&
    ['caught_up', 'timed_out'].every(key => ['true', 'false'].includes(member(page, key)?.raw));
}

export function parseWireJSON(text) {
  if (typeof text !== 'string') throw new TypeError('Expected JSON text');
  const exact = transformJSON(text, (key, raw, depth, opaque, node) => {
    if (!opaque && raw === 'null' && nullableEventRevision(node)) return raw;
    // String values are user content (for example a label named revision).
    if (!opaque && (key === 'revision' || key === 'brief_revision') && raw[0] !== '"') return JSON.stringify(decimalRevision(raw));
    return raw;
  });
  return JSON.parse(exact);
}

export function stringifyWireJSON(value) {
  const text = JSON.stringify(value);
  if (text === undefined) return text;
  // The operator's only outbound revision field is ClaimRequest's top-level
  // expect_brief_revision. Nested labels/data and arbitrary strings stay text.
  return transformJSON(text, (key, raw, depth) => key === 'expect_brief_revision' && depth === 1
    ? decimalRevision(JSON.parse(raw)) : raw);
}

export function compareRevision(a, b) {
  const left = BigInt(decimalRevision(a)), right = BigInt(decimalRevision(b));
  return left < right ? -1 : left > right ? 1 : 0;
}
