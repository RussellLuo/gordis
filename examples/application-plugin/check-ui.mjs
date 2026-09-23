// Exercise real built UI modules with the real PluginManager. This checks
// activation/registration, not browser rendering; styles are already validated
// by the bundle checks and are represented by no-op disposers here.
import assert from 'node:assert/strict';
import { mkdtemp, readFile, readdir, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from './web/node_modules/esbuild/lib/main.js';

const root = dirname(fileURLToPath(import.meta.url));
const dist = resolve(process.argv[2] ?? join(root, 'dist'));
const temp = await mkdtemp(join(tmpdir(), 'gordis-application-plugin-check-'));
try {
  await writeFile(join(temp, 'package.json'), '{"type":"module"}');
  const { imports } = JSON.parse(await readFile(join(dist, 'web/build.json'), 'utf8'));
  const mapped = source => source.replace(/from\s*(["'])([^"']+)\1/g,
    (match, _quote, id) => imports[id] ? `from '${pathToFileURL(join(temp, imports[id])).href}'` : match);
  for (const file of Object.values(imports)) {
    await mkdir(dirname(join(temp, file)), { recursive: true });
    await writeFile(join(temp, file), mapped(await readFile(join(dist, 'web', file), 'utf8')));
  }
  await build({ entryPoints: [join(root, 'web/src/loader.ts')], outfile: join(temp, 'loader.js'), bundle: true, format: 'esm' });
  const { PluginManager } = await import(pathToFileURL(join(temp, 'loader.js')));
  let checked = 0;
  const built = await readdir(join(dist, 'packages'), { withFileTypes: true }).catch(error => {
    if (error.code === 'ENOENT') return [];
    throw error;
  });
  for (const pkg of built) {
    if (!pkg.isDirectory() || !/^[a-z][a-z0-9-]*$/.test(pkg.name)) continue;
    for (const version of await readdir(join(dist, 'packages', pkg.name))) {
      const directory = join(dist, 'packages', pkg.name, version);
      const bundle = JSON.parse(await readFile(join(directory, 'bundle.json'), 'utf8'));
      const local = join(temp, pkg.name, version);
      for (const file of Object.keys(bundle.files).filter(file => file.endsWith('.js'))) {
        await mkdir(dirname(join(local, file)), { recursive: true });
        await writeFile(join(local, file), mapped(await readFile(join(directory, file), 'utf8')));
      }
      const manifest = { ...bundle.ui, entry: pathToFileURL(join(local, bundle.ui.entry)).href };
      const manager = new PluginManager(entry => import(entry), () => ({ ready: Promise.resolve(), dispose() {} }));
      await manager.reconcile([manifest]);
      assert.deepEqual([...manager.errors], [], `${pkg.name}@${version} activation failed`);
      assert.ok(manager.loaded.has(pkg.name));
      const contributions = manager.loaded.get(pkg.name).contributions;
      assert.ok(contributions.length > 0, 'example UI must register contributions');
      console.log(`PASS ${pkg.name}@${version}: ${contributions.map(c => `${c.kind}/${c.id}`).join(', ')}`);
      await manager.reconcile([]);
      assert.equal(manager.loaded.size, 0);
      checked++;
    }
  }
  assert.ok(checked > 0, 'no built UI packages found');
} finally {
  await rm(temp, { recursive: true, force: true });
}
