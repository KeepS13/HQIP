import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

// Execute the actual hook with deferred API responses and deterministic hooks.
// The harness supports rerenders, path changes, cleanup and real promise order.
function harness() {
  let cursor = 0, path = '/orders/first', value;
  const slots = [], effects = [], requests = [], timers = new Set(), listeners = new Map();
  const same = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const react = {
    useState(initial) {
      const i = cursor++;
      if (!slots[i]) slots[i] = { value: initial };
      return [slots[i].value, v => { slots[i].value = typeof v === 'function' ? v(slots[i].value) : v; }];
    },
    useRef(initial) {
      const i = cursor++;
      return slots[i] ||= { current: initial };
    },
    useCallback(fn, deps) {
      const i = cursor++;
      if (!slots[i] || !same(slots[i].deps, deps)) slots[i] = { fn, deps };
      return slots[i].fn;
    },
    useEffect(fn, deps) {
      const i = cursor++;
      if (!slots[i] || !same(slots[i].deps, deps)) effects.push(() => {
        slots[i]?.cleanup?.();
        slots[i] = { deps, cleanup: fn() };
      });
    },
  };
  const api = { userAPI: requestedPath => new Promise((resolve, reject) => requests.push({ path: requestedPath, resolve, reject })) };
  const context = { exports: {}, require: name => name === 'react' ? react : api, Date, URL,
    window: { setInterval(fn) { timers.add(fn); return fn; }, clearInterval(fn) { timers.delete(fn); } },
    document: { hidden: false, addEventListener(name, fn) { listeners.set(name, fn); }, removeEventListener(name) { listeners.delete(name); } },
  };
  const source = readFileSync(new URL('../src/portal/payment-data.ts', import.meta.url), 'utf8');
  vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context);
  function render(next = path) {
    path = next;
    cursor = 0;
    value = context.exports.usePaymentData(path);
    effects.splice(0).forEach(fn => fn());
    return value;
  }
  render();
  return { requests, render, tick: () => timers.forEach(fn => fn()), visible: () => listeners.get('visibilitychange')?.(),
    unmount: () => slots.forEach(slot => slot?.cleanup?.()),
    async settle(index, result, failed = false) {
      requests[index][failed ? 'reject' : 'resolve'](result);
      // Await across the VM realm's promise queue as well as the test realm.
      await new Promise(setImmediate);
      return render();
    },
  };
}

test('slow requests survive multiple polling periods and visibility events', async () => {
  const h = harness();
  for (let i = 0; i < 6; i++) { h.tick(); h.visible(); }
  assert.equal(h.requests.length, 1);
  const result = await h.settle(0, { status: 'paid' });
  assert.equal(result.data.status, 'paid');
  assert.equal(result.loading, false);
  assert.equal(result.refreshing, false);
  h.tick();
  assert.equal(h.requests.length, 2);
  const failed = await h.settle(1, new Error('temporarily offline'), true);
  assert.equal(failed.data.status, 'paid');
  assert.equal(failed.error, 'temporarily offline');
  h.tick();
  assert.equal(h.requests.length, 3);
  h.unmount();
});

test('explicit refresh supersedes an old poll without restoring unpaid data', async () => {
  const h = harness();
  const refresh = h.render().refresh();
  assert.equal(h.requests.length, 2);
  await h.settle(0, { status: 'unpaid' });
  h.tick();
  assert.equal(h.requests.length, 2, 'old finally must not clear the new pending request');
  await h.settle(1, { status: 'paid' });
  await refresh;
  assert.equal(h.render().data.status, 'paid');
  h.unmount();
});

test('changing routes rejects the previous result and retains the new request guard', async () => {
  const h = harness();
  h.render('/orders/second');
  assert.equal(h.requests.length, 2);
  await h.settle(0, { id: 'first' });
  h.tick();
  assert.equal(h.requests.length, 2);
  assert.equal(h.render().data, null);
  await h.settle(1, { id: 'second' });
  assert.equal(h.render().data.id, 'second');
  h.unmount();
  h.tick();
  h.visible();
  assert.equal(h.requests.length, 2);
});
