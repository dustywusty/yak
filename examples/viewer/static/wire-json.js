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
  const edits = [];
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
  function value(key, depth, opaque = false) {
    space();
    const start = pos;
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
        value(name, depth + 1, childOpaque); space();
        if (text[pos] !== ',') break;
        pos++;
      }
      take('}');
    } else if (text[pos] === '[') {
      pos++; space();
      if (text[pos] !== ']') for (;;) {
        value(undefined, depth + 1, opaque); space();
        if (text[pos] !== ',') break;
        pos++;
      }
      take(']');
    } else if (text[pos] === '"') string();
    else {
      while (pos < text.length && !/[\t\n\r ,}\]]/.test(text[pos])) pos++;
      if (pos === start) throw new SyntaxError('Expected JSON value');
    }
    const raw = text.slice(start, pos), replacement = transform(key, raw, depth, opaque);
    if (replacement !== raw) edits.push({ start, end: pos, replacement });
  }
  value(undefined, 0); space();
  if (pos !== text.length) throw new SyntaxError('Trailing JSON data');
  // Transforms only replace primitive revision values, so edits cannot overlap.
  let result = '', end = 0;
  for (const edit of edits) { result += text.slice(end, edit.start) + edit.replacement; end = edit.end; }
  return result + text.slice(end);
}

export function parseWireJSON(text) {
  if (typeof text !== 'string') throw new TypeError('Expected JSON text');
  const exact = transformJSON(text, (key, raw, depth, opaque) => {
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
