// Skin host: mounts one skin (#/dispatch, #/wall, #/irc) on one board.
// Board: ?b=<board id>, else the first readable board.

import { createStore, h } from './skin-kit.js';
import { dispatch } from './dispatch.js';
import { wall } from './wall.js';

const SKINS = { dispatch, wall };

export function createSkins({ api, setStatus, onUnauthorized }) {
  let cur = null; // { key, store, view, timer }
  const root = () => document.getElementById('skin');

  async function mount(route) {
    let boardId = route.board;
    if (!boardId) {
      const { boards } = await api('GET', '/v1/boards');
      boardId = boards[0]?.id;
      if (!boardId) { unmount(); root().replaceChildren(h('p', { class: 'pad' }, 'No visible boards.')); return; }
    }
    for (const a of document.getElementById('nav').children) a.classList.toggle('sel', a.dataset.view === `skin-${route.skin}`);
    const key = `${route.skin}:${boardId}`;
    if (cur?.key === key) return;
    unmount();
    const el = root();
    el.className = `skin skin-${route.skin}`;
    el.replaceChildren(h('p', { class: 'pad' }, 'Loading board…'));
    const view = SKINS[route.skin](el, { boardId, threadHref: (t) => `#/b/${encodeURIComponent(boardId)}?t=${encodeURIComponent(t)}` });
    const store = createStore({
      api, boardId, onUnauthorized,
      onStatus: setStatus,
      onChange: () => view.render(store),
      onEvent: (ev) => view.event?.(ev, store),
    });
    const timer = setInterval(() => store.S.board && view.tick?.(store), 1000); // lease countdowns
    cur = { key, store, view, timer };
  }

  function unmount() {
    if (!cur) return;
    cur.store.stop();
    clearInterval(cur.timer);
    cur.view.dispose?.();
    cur = null;
    setStatus(null);
  }

  return { mount, unmount };
}
