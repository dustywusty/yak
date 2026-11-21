// Synthetic requests only: each test installs the real fixture in a fresh VM.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runInNewContext } from 'node:vm';
import { operatorFixture } from './operator-fixture.mjs';

function fixture() {
  const window = {};
  runInNewContext(`(${operatorFixture})();`, {
    window, location: { href: 'http://synthetic.invalid/' },
    URL, Response, DOMException, Date, setTimeout,
  });
  return {
    state: window.__op,
    snapshot: (signal) => window.fetch('/v1/boards/brd_test/snapshots', { method: 'POST', signal }),
    events: (signal, cursor = 'cur') => window.fetch(
      `/v1/boards/brd_test/events?${new URLSearchParams(cursor ? { cursor } : { from: 'latest' })}`,
      { signal }),
  };
}

async function receivesResync(f, signal) {
  const response = await f.events(signal);
  assert.equal(response.status, 409);
  assert.deepEqual(await response.json(), { code: 'resync_required' });
  assert.equal(f.state.resync, false);
  assert.equal((await f.events(signal)).status, 200, 'store receives exactly one 409');
}

for (const order of [['feed', 'store'], ['store', 'feed']]) {
  test(`store resync is delivered exactly once with ${order.join('-first then ')}`, async () => {
    const f = fixture();
    const signals = { store: new AbortController().signal, feed: new AbortController().signal };
    assert.notEqual(signals.store, signals.feed);
    await f.snapshot(signals.store);
    f.state.resync = true;
    for (const caller of order) {
      if (caller === 'store') await receivesResync(f, signals.store);
      else assert.equal((await f.events(signals.feed)).status, 200);
    }
    assert.equal((await f.events(signals.feed)).status, 200);
    assert.equal(f.state.snapshots, 1);
  });
}

test('requests before a snapshot cannot consume an armed injection', async () => {
  const f = fixture(), store = new AbortController().signal;
  f.state.resync = true;
  assert.equal((await f.events(store)).status, 200);
  assert.equal((await f.events()).status, 200);
  assert.equal(f.state.resync, true);
  await f.snapshot(store);
  await receivesResync(f, store);
});

for (const absent of [undefined, null]) {
  test(`snapshot with ${absent} signal does not own the injection`, async () => {
    const f = fixture(), store = new AbortController().signal;
    await f.snapshot(absent);
    f.state.resync = true;
    assert.equal((await f.events(absent)).status, 200);
    assert.equal((await f.events(store)).status, 200);
    assert.equal(f.state.resync, true);
    await f.snapshot(store);
    await receivesResync(f, store);
  });
}

test('owner requests without a cursor and requests without a signal cannot consume resync', async () => {
  const f = fixture(), store = new AbortController().signal;
  await f.snapshot(store);
  f.state.resync = true;
  assert.equal((await f.events(store, '')).status, 200);
  assert.equal((await f.events()).status, 200);
  assert.equal((await f.events(null)).status, 200);
  assert.equal(f.state.resync, true);
  await receivesResync(f, store);
});

for (const owner of [true, false]) {
  test(`aborted ${owner ? 'owner' : 'feed'} request cannot consume resync`, async () => {
    const f = fixture(), store = new AbortController(), feed = new AbortController();
    await f.snapshot(store.signal);
    f.state.resync = true;
    const canceled = owner ? store : feed;
    canceled.abort();
    await assert.rejects(f.events(canceled.signal), { name: 'AbortError' });
    assert.equal(f.state.resync, true);
    if (owner) {
      const replacement = new AbortController();
      await f.snapshot(replacement.signal);
      await receivesResync(f, replacement.signal);
    } else await receivesResync(f, store.signal);
  });
}

test('latest snapshot replaces ownership even while the old signal is live', async () => {
  const f = fixture(), old = new AbortController(), replacement = new AbortController();
  await f.snapshot(old.signal);
  f.state.resync = true;
  await f.snapshot(replacement.signal);
  assert.equal(old.signal.aborted, false);
  assert.equal((await f.events(old.signal)).status, 200);
  assert.equal(f.state.resync, true);
  await receivesResync(f, replacement.signal);
  assert.equal(f.state.snapshots, 2);
});

test('resnapshot with the same signal retains ownership for a newly armed injection', async () => {
  const f = fixture(), store = new AbortController().signal;
  await f.snapshot(store);
  f.state.resync = true;
  await receivesResync(f, store);
  await f.snapshot(store);
  assert.equal((await f.events(store)).status, 200);
  f.state.resync = true;
  await receivesResync(f, store);
});

test('replacement snapshot without a signal clears previous ownership', async () => {
  const f = fixture(), old = new AbortController().signal;
  await f.snapshot(old);
  await f.snapshot();
  f.state.resync = true;
  assert.equal((await f.events(old)).status, 200);
  assert.equal((await f.events()).status, 200);
  assert.equal(f.state.resync, true);
});

test('aborted snapshot cannot replace the current owner', async () => {
  const f = fixture(), store = new AbortController().signal, canceled = new AbortController();
  await f.snapshot(store);
  f.state.resync = true;
  canceled.abort();
  await assert.rejects(f.snapshot(canceled.signal), { name: 'AbortError' });
  assert.equal(f.state.snapshots, 1);
  await receivesResync(f, store);
});
