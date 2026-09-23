import type { Contribution, Dispose, PackageManifest, PluginModule } from '@gordis/example-ui';

export interface LoadedPlugin { manifest: PackageManifest; contributions: Contribution[]; disposers: Dispose[] }
interface Stylesheet { ready: Promise<void>; dispose: Dispose }
export const message = (error: unknown): string => error instanceof Error ? error.message : String(error);
const fingerprint = (p: PackageManifest) => JSON.stringify([p.version, p.entry, p.styles, p.apiVersion]);

// Loaded once per package, regardless of the number or generation of instances.
export class PluginManager {
  readonly loaded = new Map<string, LoadedPlugin>();
  readonly errors = new Map<string, string>();
  private failed = new Map<string, string>();
  private queue: Promise<void> = Promise.resolve();
  constructor(private importModule: (entry: string) => Promise<PluginModule>,
    private loadStyle: (url: string) => Stylesheet,
    private beforeUnload: (id: string) => void = () => {}) {}

  retry(id: string): void { this.failed.delete(id); }
  reconcile(packages: PackageManifest[]): Promise<void> {
    const snapshot = packages.map(p => ({ ...p, styles: [...p.styles] }));
    const next = this.queue.then(() => this.apply(snapshot));
    this.queue = next.catch(() => {});
    return next;
  }
  private dispose(plugin: LoadedPlugin): void {
    for (const fn of plugin.disposers.reverse()) {
      try { fn(); } catch (error) { this.errors.set(plugin.manifest.id, `卸载失败：${message(error)}`); }
    }
    plugin.disposers.length = 0;
  }
  private async apply(packages: PackageManifest[]): Promise<void> {
    const desired = new Map(packages.map(p => [p.id, p]));
    for (const [id, plugin] of this.loaded) {
      const next = desired.get(id);
      if (!next || fingerprint(next) !== fingerprint(plugin.manifest)) {
        // The host synchronously unmounts React trees before removing styles
        // and activation resources. Each component's effects run their cleanup.
        this.beforeUnload(id);
        this.dispose(plugin);
        this.loaded.delete(id);
      }
    }
    for (const id of this.failed.keys()) {
      if (!desired.has(id)) { this.failed.delete(id); this.errors.delete(id); }
    }
    for (const manifest of packages) {
      if (this.loaded.has(manifest.id) || this.failed.get(manifest.id) === fingerprint(manifest)) continue;
      this.errors.delete(manifest.id);
      const plugin: LoadedPlugin = { manifest, contributions: [], disposers: [] };
      let accepting = true;
      try {
        if (manifest.apiVersion !== 2) throw new Error(`不支持 UI API ${manifest.apiVersion}`);
        for (const href of manifest.styles) {
          const style = this.loadStyle(href);
          plugin.disposers.push(style.dispose);
          await style.ready;
        }
        const module = await this.importModule(manifest.entry);
        if (module.apiVersion !== 2 || typeof module.activate !== 'function') throw new Error('UI 模块契约不兼容');
        await module.activate({
          manifest: Object.freeze({ ...manifest, styles: [...manifest.styles] }),
          register: contribution => {
            if (!accepting) throw new Error('插件注册阶段已结束');
            if (!/^[a-z][a-z0-9-]*$/.test(contribution.id) || !['page', 'instance-panel'].includes(contribution.kind)) throw new Error('无效的 UI 贡献');
            if (plugin.contributions.some(c => c.id === contribution.id)) throw new Error(`重复的贡献 ID：${contribution.id}`);
            plugin.contributions.push({ ...contribution });
          },
          defer: dispose => {
            if (!accepting) { dispose(); throw new Error('插件注册阶段已结束'); }
            plugin.disposers.push(dispose);
          },
        });
        this.loaded.set(manifest.id, plugin);
        this.failed.delete(manifest.id);
      } catch (error) {
        this.dispose(plugin);
        this.failed.set(manifest.id, fingerprint(manifest));
        this.errors.set(manifest.id, message(error));
      } finally { accepting = false; }
    }
  }
}
