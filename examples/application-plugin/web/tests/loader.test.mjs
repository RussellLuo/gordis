import test from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';

const compiled = await build({ entryPoints: [new URL('../src/loader.ts', import.meta.url).pathname],
  bundle: true, write: false, format: 'esm' });
const { PluginManager } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].contents).toString('base64')}`);

const manifest = (id = 'counter', version = '1') => ({ id, version, entry: `/demo/assets/plugins/${id}/${version}/index.js`, title: id, apiVersion: 2, activation: 'backend', styles: [] });
const style = () => ({ ready: Promise.resolve(), dispose() {} });
const page = { id: 'overview', kind: 'page', title: 'Overview', component: () => null };
const panel = { id: 'reading', kind: 'instance-panel', title: 'Reading', component: () => null };

test('one package registers a shared page and instance panel, with one activation', async () => {
  let activations = 0;
  const disposed = [];
  const manager = new PluginManager(async () => ({ apiVersion: 2, activate(ctx) {
    activations++; ctx.register(page); ctx.register(panel); ctx.defer(() => disposed.push('package'));
  } }), style);
  await manager.reconcile([manifest()]);
  // Instance membership/generation changes are supplied to React separately.
  await manager.reconcile([manifest()]);
  assert.equal(activations, 1);
  assert.equal(manager.loaded.get('counter').contributions.length, 2);
  await manager.reconcile([]); await manager.reconcile([]);
  assert.deepEqual(disposed, ['package']);
});

test('unloading one backend package leaves another active', async () => {
  const manager = new PluginManager(async () => ({ apiVersion: 2, activate: ctx => ctx.register(page) }), style);
  await manager.reconcile([manifest(), manifest('other')]);
  await manager.reconcile([manifest('other')]);
  assert.deepEqual([...manager.loaded.keys()], ['other']);
});

test('version change unmounts both contributions before activation and CSS disposal', async () => {
  const events = [];
  const manager = new PluginManager(async () => ({ apiVersion: 2, activate(ctx) {
    events.push(`start ${ctx.manifest.version}`); ctx.register(page); ctx.register(panel); ctx.defer(() => events.push('dispose'));
  } }), () => ({ ready: Promise.resolve(), dispose: () => events.push('css removed') }), () => events.push('unmount all'));
  await manager.reconcile([{ ...manifest(), styles: ['style.css'] }]);
  await manager.reconcile([manifest('counter', '2')]);
  assert.deepEqual(events, ['start 1', 'unmount all', 'dispose', 'css removed', 'start 2']);
});

test('duplicate contribution fails activation and rolls back registrations and CSS', async () => {
  let cleaned = 0;
  const manager = new PluginManager(async () => ({ apiVersion: 2, activate(ctx) {
    ctx.defer(() => cleaned++); ctx.register(page); ctx.register(page);
  } }), () => ({ ready: Promise.resolve(), dispose: () => cleaned++ }));
  await manager.reconcile([{ ...manifest(), styles: ['style.css'] }]);
  assert.equal(cleaned, 2); assert.equal(manager.loaded.size, 0);
  assert.match(manager.errors.get('counter'), /重复/);
});

test('API mismatch and import failure allow explicit retry without affecting other packages', async () => {
  let imports = 0;
  const manager = new PluginManager(async entry => {
    imports++;
    if (entry.includes('broken')) throw new Error('asset missing');
    return { apiVersion: 2, activate: ctx => ctx.register(page) };
  }, style);
  await manager.reconcile([{ ...manifest(), apiVersion: 99 }]); assert.equal(imports, 0);
  await manager.reconcile([manifest('broken'), manifest('other')]);
  assert.equal(manager.loaded.size, 1); assert.match(manager.errors.get('broken'), /asset missing/);
  const before = imports; await manager.reconcile([manifest('broken'), manifest('other')]); assert.equal(imports, before);
  manager.retry('broken'); await manager.reconcile([manifest('broken'), manifest('other')]); assert.equal(imports, before + 1);
});

test('stylesheet failure disposes partial resources and leaves another package available', async () => {
  let removed = 0;
  const manager = new PluginManager(async () => ({ apiVersion: 2, activate: ctx => ctx.register(page) }), () => ({
    ready: Promise.reject(new Error('CSS missing')), dispose: () => removed++,
  }));
  await manager.reconcile([{ ...manifest(), styles: ['missing.css'] }, manifest('other')]);
  assert.equal(removed, 1); assert.equal(manager.loaded.size, 1);
  assert.match(manager.errors.get('counter'), /CSS missing/);
});

test('late import is disposed before a later reconcile completes', async () => {
  let resolveImport;
  let disposed = 0;
  const pending = new Promise(resolve => { resolveImport = resolve; });
  const manager = new PluginManager(() => pending, style);
  const first = manager.reconcile([manifest()]);
  const next = manager.reconcile([]);
  resolveImport({ apiVersion: 2, activate(ctx) { ctx.register(page); ctx.defer(() => disposed++); } });
  await Promise.all([first, next]);
  assert.equal(manager.loaded.size, 0); assert.equal(disposed, 1);
});
