import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

// Run both production hooks with controlled fetches and timers. The shared
// scheduler has no React dependency; each app retains its own API/auth adapter.
function harness(app) {
  let cursor = 0, path = '/first', value, now = 0;
  const slots = [], effects = [], requests = [], events = [], timers = new Map(), listeners = new Map();
  const same = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const react = {
    useState(initial) {
      const i = cursor++;
      if (!slots[i]) slots[i] = { value: initial };
      return [slots[i].value, v => { slots[i].value = typeof v === 'function' ? v(slots[i].value) : v; }];
    },
    useRef(initial) { const i = cursor++; return slots[i] ||= { current: initial }; },
    useCallback(fn, deps) {
      const i = cursor++;
      if (!slots[i] || !same(slots[i].deps, deps)) slots[i] = { fn, deps };
      return slots[i].fn;
    },
    useEffect(fn, deps) {
      const i = cursor++;
      if (!slots[i] || !same(slots[i].deps, deps)) effects.push(() => {
        slots[i]?.cleanup?.(); slots[i] = { deps, cleanup: fn() };
      });
    },
  };
  let nextTimer = 0;
  const setTimer = (fn, delay, interval = false) => {
    const id = ++nextTimer; timers.set(id, { fn, at: now + delay, delay, interval }); return id;
  };
  const context = {
    AbortController, Error, Promise, Event,
    setTimeout: (fn, delay) => setTimer(fn, delay),
    setInterval: (fn, delay) => setTimer(fn, delay, true),
    clearTimeout: id => timers.delete(id), clearInterval: id => timers.delete(id),
    document: {
      hidden: false,
      addEventListener(name, fn) { listeners.set(name, fn); },
      removeEventListener(name, fn) { if (listeners.get(name) === fn) listeners.delete(name); },
    },
    fetch: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject })),
  };
  context.window = { ...context, dispatchEvent(event) { events.push(event.type); } };
  function load(url) {
    const scope = { ...context, exports: {}, require(name) {
      if (name === 'react') return react;
      if (name.endsWith('config')) return { API_URL: '' };
      return load(new URL(name + '.ts', url));
    } };
    vm.runInNewContext(ts.transpileModule(readFileSync(url, 'utf8'), {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    }).outputText, scope);
    return scope.exports;
  }
  const module = load(new URL(app === 'user' ? '../src/portal/api.ts' : '../../admin-web/src/api.ts', import.meta.url));
  function render(next = path) {
    path = next; cursor = 0;
    value = (module.useUserData || module.usePolling)(path, 5000);
    effects.splice(0).forEach(fn => fn()); return value;
  }
  render();
  return {
    requests, events, render,
    advance(ms) {
      now += ms;
      for (const [id, timer] of [...timers]) {
        if (timer.at > now) continue;
        timers.delete(id);
        if (timer.interval) timers.set(id, { ...timer, at: now + timer.delay });
        timer.fn();
      }
    },
    visible(hidden = false) { context.document.hidden = hidden; listeners.get('visibilitychange')?.(); },
    unmount() { slots.forEach(slot => slot?.cleanup?.()); },
    async settle(index, data, status = 200) {
      requests[index].resolve({ ok: status < 400, status, json: async () => data });
      await new Promise(setImmediate); return render();
    },
    async flush() { await new Promise(setImmediate); return render(); },
  };
}

for (const app of ['user', 'admin']) {
  test(`${app}: slow polling and visibility events never overlap`, async () => {
    const h = harness(app);
    for (let i = 0; i < 2; i++) { h.advance(5000); h.visible(); }
    assert.equal(h.requests.length, 1);
    await h.settle(0, { version: 1 });
    h.advance(4999); assert.equal(h.requests.length, 1);
    h.advance(1); assert.equal(h.requests.length, 2);
    h.unmount();
  });
  test(`${app}: refresh after a write queues one new read and discards old data`, async () => {
    const h = harness(app);
    const first = h.render().refresh(), second = h.render().refresh();
    assert.equal(h.requests.length, 1);
    await h.settle(0, { version: 'before-write' });
    assert.equal(h.render().data, null);
    assert.equal(h.requests.length, 2);
    await h.settle(1, { version: 'after-write' });
    await Promise.all([first, second]);
    assert.equal(h.render().data.version, 'after-write'); h.unmount();
  });
  test(`${app}: route cleanup aborts and late results cannot overwrite a new route`, async () => {
    const h = harness(app);
    h.render('/second');
    assert.equal(h.requests[0].options.signal.aborted, true);
    await h.settle(0, { id: 'first' });
    assert.equal(h.render().data, null);
    await h.settle(1, { id: 'second' });
    assert.equal(h.render().data.id, 'second');
    h.unmount(); h.advance(10000); h.visible(); assert.equal(h.requests.length, 2);
  });
  test(`${app}: failures back off, preserve data, and hidden pages stop polling`, async () => {
    const h = harness(app);
    await h.settle(0, { version: 1 });
    h.advance(5000);
    await h.settle(1, { error: 'offline' }, 503);
    assert.equal(h.render().data.version, 1);
    assert.equal(h.render().error, 'offline');
    h.advance(5000); assert.equal(h.requests.length, 2);
    h.advance(5000); assert.equal(h.requests.length, 3);
    await h.settle(2, { version: 2 });
    h.visible(true); h.advance(60000); assert.equal(h.requests.length, 3);
    h.visible(); assert.equal(h.requests.length, 4); h.unmount();
  });
  test(`${app}: timed out reads abort and late data stays discarded`, async () => {
    const h = harness(app);
    h.advance(15000);
    assert.equal(h.requests[0].options.signal.aborted, true);
    await h.settle(0, { version: 'too-late' });
    assert.equal(h.render().data, null);
    assert.match(h.render().error, /超时/);
    h.advance(10000); assert.equal(h.requests.length, 2); h.unmount();
  });
  test(`${app}: cancelled old response cannot expire the current login`, async () => {
    const h = harness(app);
    let finishBody;
    h.requests[0].resolve({ ok: false, status: 401, json: () => new Promise(resolve => { finishBody = resolve; }) });
    await h.flush();
    h.render('/second');
    finishBody({ error: 'old login expired' });
    await h.flush();
    assert.deepEqual(h.events, []);
    h.unmount();
  });
}
