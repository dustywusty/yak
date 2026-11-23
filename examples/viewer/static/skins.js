// Skin host: mounts one skin (#/board, #/forum; #/wall remains an alias) on one board.
// Board: ?b=<board id>, else the first readable board.

import { createStore, h } from './skin-kit.js';
import { wall } from './wall.js';
import { forum } from './forum.js';

const SKINS = { board: wall, forum };

export function createSkins({ api, setStatus, onUnauthorized }) {
  let cur = null; // { key, store, view, timer }
  const root = () => document.getElementById('skin');

  // Keep negative visibility decisions across Forum mounts in this login session.
  const deniedByBoard = new Map();
  let lookup;
  async function mount(route) {
    lookup?.abort();
    let boardId = route.board;
    if (!boardId) {
      lookup = new AbortController();
      const { boards } = await api('GET', '/v1/boards', {}, lookup.signal);
      boardId = boards[0]?.id;
      if (!boardId) { unmount(); root().replaceChildren(h('p', { class: 'pad' }, 'No visible boards.')); return; }
    }
    if (route.skin === 'forum' && !route.board) {
      const query = new URLSearchParams(location.hash.split('?')[1]);
      query.set('b', boardId);
      history.replaceState(null, '', '#/forum?' + query);
    }
    for (const a of document.getElementById('nav').children) {
      a.classList.toggle('sel', a.dataset.view === `skin-${route.skin}`);
      if (a.dataset.view === 'term') a.href = `#/b/${encodeURIComponent(boardId)}`;
      if (a.dataset.view.startsWith('skin-')) a.href = `#/${a.dataset.view.slice(5)}?b=${encodeURIComponent(boardId)}`;
    }
    const key = `${route.skin}:${boardId}`;
    if (cur?.key === key) { cur.view.navigate?.(route, cur.store); return; }
    unmount();
    document.body.dataset.skin = route.skin;
    const el = root();
    el.className = `skin skin-${route.skin}`;
    el.replaceChildren(h('p', { class: 'pad' }, 'Loading board…'));
    if (route.skin === 'forum' && !deniedByBoard.has(boardId)) deniedByBoard.set(boardId, new Set());
    const view = SKINS[route.skin](el, { boardId, route, deniedThreads: deniedByBoard.get(boardId), threadHref: (t) => `#/b/${encodeURIComponent(boardId)}?t=${encodeURIComponent(t)}` });
    const store = createStore({
      api, boardId, onUnauthorized,
      onStatus: (status) => { setStatus(status); view.status?.(status, store); },
      onChange: () => view.render(store),
      onEvent: (ev) => view.event?.(ev, store),
    });
    const timer = view.tick && setInterval(() => store.S.board && view.tick(store), 1000);
    cur = { key, store, view, timer };
  }

  function unmount() {
    lookup?.abort();
    lookup = null;
    if (!cur) return;
    cur.store.stop();
    clearInterval(cur.timer);
    cur.view.dispose?.();
    root().replaceChildren();
    delete document.body.dataset.skin;
    cur = null;
    setStatus(null);
  }

  return { mount, unmount, endSession() { unmount(); deniedByBoard.clear(); } };
}
