// Backend and browser plugin remain separate build graphs.
import { buildHost, buildPlugin } from './web/build.mjs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const dist = resolve(process.argv.find(arg => arg.startsWith('--output='))?.slice(9) ?? join(root, 'dist'));
const env = { ...process.env, GOPROXY: 'off', GOWORK: join(root, 'go.work') };
const digest = data => createHash('sha256').update(data).digest('hex');
const go = args => execFileSync('go', args, { cwd: root, env, stdio: 'inherit' });
const onlyHost = process.argv.includes('--host-only');
const onlyPackages = process.argv.includes('--packages-only');
if (onlyHost && onlyPackages) throw new Error('Choose one build mode');
await mkdir(dist, { recursive: true });
if (!onlyPackages) {
  const web = join(dist, 'web');
  const imports = await buildHost(web);
  await writeFile(join(web, 'build.json'), JSON.stringify({ imports }) + '\n');
  go(['build', '-trimpath', '-o', join(dist, 'host'), './host']);
  console.log('Built host and shared UI runtime.');
}
if (!onlyHost) {
  await mkdir(join(dist, 'packages'), { recursive: true });
  const selected = process.argv.find(arg => arg.startsWith('--version='))?.slice(10);
  for (const dir of (await readdir(join(root, 'plugins'), { withFileTypes: true })).filter(d => d.isDirectory())) {
    const source = join(root, 'plugins', dir.name);
    const descriptor = JSON.parse(await readFile(join(source, 'package.json'), 'utf8'));
    const metadata = JSON.parse(await readFile(join(source, 'plugin.json'), 'utf8'));
    if (!/^[a-z][a-z0-9]*(-[a-z0-9]+)*$/.test(metadata.id) || metadata.id !== dir.name) throw new Error('Invalid plugin directory/ID');
    const versions = selected ? [selected] : descriptor.versions;
    for (const version of versions) {
      if (!/^[0-9]+\.[0-9]+\.[0-9]+$/.test(version) || !descriptor.versions.includes(version)) throw new Error(`Unsupported version for ${metadata.id}: ${version}`);
      const stage = await mkdtemp(join(dist, '.package-'));
      try {
        await mkdir(join(stage, 'backend'));
        go(['build', '-trimpath', '-ldflags', `-X main.version=${version}`, '-o', join(stage, 'backend/plugin'), metadata.backend]);
        const backendSHA256 = digest(await readFile(join(stage, 'backend/plugin')));
        await writeFile(join(stage, 'backend/manifest.json'), JSON.stringify({
          id: metadata.id, version, entry: 'plugin', sha256: backendSHA256,
          protocol: 'gordis.process/1', contracts: ['gordis.lifecycle/1', 'gordis.application.endpoints/1'],
        }, null, 2) + '\n');
        const ui = { ...await buildPlugin(source, join(stage, 'ui')), version };
        const files = {};
        async function collect(directory, prefix = '') {
          for (const entry of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
            const name = prefix + entry.name;
            if (entry.isDirectory()) await collect(join(directory, entry.name), name + '/');
            else files['ui/' + name] = digest(await readFile(join(directory, entry.name)));
          }
        }
        await collect(join(stage, 'ui/plugins'), 'plugins/');
        ui.entry = 'ui/' + ui.entry;
        ui.styles = ui.styles.map(file => 'ui/' + file);
        const bundle = JSON.stringify({ id: metadata.id, version, instances: descriptor.instances,
          backend: 'backend/manifest.json', backendSHA256, ui, files }, null, 2) + '\n';
        await writeFile(join(stage, 'bundle.json'), bundle);
        await mkdir(join(dist, 'packages', metadata.id), { recursive: true });
        const destination = join(dist, 'packages', metadata.id, version);
        let existing;
        try { existing = await readFile(join(destination, 'bundle.json'), 'utf8'); }
        catch (error) { if (error.code !== 'ENOENT') throw error; }
        if (existing !== undefined) {
          if (existing !== bundle) throw new Error(`Immutable package already exists: ${destination}. Stop the demo and remove that build directory before rebuilding changed source.`);
        } else await rename(stage, destination);
        console.log(`Package ${version}: ${destination}`);
      } finally { await rm(stage, { recursive: true, force: true }); }
    }
  }
}
