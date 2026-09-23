import { build } from 'esbuild';
import { createHash } from 'node:crypto';
import { copyFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const root = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
export const sharedModules = ['react', 'react-dom', 'react-dom/client', 'react/jsx-runtime', '@gordis/example-ui'];
const options = { bundle: true, format: 'esm', target: 'es2022', jsx: 'automatic', define: { 'process.env.NODE_ENV': '"production"' }, logLevel: 'warning' };

// Every plugin is a separate build graph. Relative TS/TSX imports, code chunks,
// CSS and images are included; only the shared runtime stays external.
export async function buildPlugin(directory, dist) {
  const manifest = JSON.parse(await readFile(join(directory, 'plugin.json'), 'utf8'));
  if (!/^[a-z][a-z0-9-]*$/.test(manifest.id)) throw new Error('Invalid plugin ID');
  const outdir = resolve(dist, '.bundle');
  const result = await build({ ...options, entryPoints: [join(directory, 'src/index.tsx')], outdir,
    external: sharedModules, splitting: true, entryNames: 'index', chunkNames: 'chunks/[name]-[hash]',
    assetNames: 'assets/[name]-[hash]', loader: { '.svg': 'file', '.png': 'file', '.jpg': 'file' },
    write: false, metafile: true,
  });
  const files = result.outputFiles.map(file => ({ name: relative(outdir, file.path), contents: file.contents })).sort((a, b) => a.name.localeCompare(b.name));
  const hash = createHash('sha256');
  for (const file of files) hash.update(file.name).update(file.contents);
  const prefix = `plugins/${manifest.id}/${hash.digest('hex').slice(0, 16)}`;
  for (const file of files) {
    const destination = join(dist, prefix, file.name);
    await mkdir(dirname(destination), { recursive: true });
    await writeFile(destination, file.contents);
  }
  // Build metadata is kept outside the immutable package assets, for tests.
  await mkdir(join(dist, 'meta'), { recursive: true });
  await writeFile(join(dist, 'meta', `${manifest.id}.json`), JSON.stringify(result.metafile, null, 2));
  return { ...manifest, entry: `${prefix}/index.js`, styles: files.filter(file => file.name.endsWith('.css')).map(file => `${prefix}/${file.name}`) };
}

export async function buildHost(dist) {
  const shared = [
    ['react', 'react', []],
    ['react/jsx-runtime', 'jsx-runtime', ['react']],
    ['react-dom', 'react-dom', ['react']],
    ['react-dom/client', 'react-dom-client', ['react', 'react-dom']],
  ];
  const imports = {};
  for (const [specifier, name, external] of shared) {
    const file = `shared/${name}.js`;
    // React ships CommonJS: explicit named exports are needed for native ESM.
    // Resolve the entry to a file so externalizing react/* cannot externalize
    // this wrapper's own entry and accidentally create a self-import.
    const names = Object.keys(require(specifier)).filter(name => /^[A-Za-z_$][\w$]*$/.test(name) && name !== 'default');
    const contents = `import runtime from ${JSON.stringify(require.resolve(specifier))}; export default runtime; export const { ${names.join(', ')} } = runtime;`;
    // esbuild preserves external CommonJS require calls. Bridge only the
    // declared shared modules to ESM imports; never bundle another React copy.
    const banner = external.length ? {
      js: external.map((id, index) => `import gordisShared${index} from ${JSON.stringify(id)};`).join('\n')
        + `\nconst gordisShared = {${external.map((id, index) => `${JSON.stringify(id)}: gordisShared${index}`).join(',')}};\n`
        + 'const require = id => { if (Object.hasOwn(gordisShared, id)) return gordisShared[id]; throw new Error(`Unknown shared module: ${id}`); };',
    } : undefined;
    await build({ ...options, stdin: { contents, resolveDir: root }, external, banner, outfile: join(dist, file) });
    imports[specifier] = file;
  }
  imports['@gordis/example-ui'] = 'shared/sdk.js';
  await build({ ...options, entryPoints: [join(root, 'src/sdk.tsx')], external: sharedModules, outfile: join(dist, imports['@gordis/example-ui']) });
  const result = await build({ ...options, entryPoints: [join(root, 'src/app.tsx')], external: sharedModules,
    outfile: join(dist, 'host/app.js'), metafile: true });
  await mkdir(join(dist, 'meta'), { recursive: true });
  await writeFile(join(dist, 'meta/host.json'), JSON.stringify(result.metafile, null, 2));
  await copyFile(join(root, 'index.html'), join(dist, 'index.html'));
  await copyFile(join(root, 'style.css'), join(dist, 'style.css'));
  return imports;
}
