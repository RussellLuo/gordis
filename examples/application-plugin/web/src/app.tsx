import { Component, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import { flushSync } from 'react-dom';
import { PluginProvider } from '@gordis/example-ui';
import type { Catalog as UICatalog, Contribution, Instance, PluginModule } from '@gordis/example-ui';
import { PluginManager, message } from './loader.js';
import type { LoadedPlugin } from './loader.js';

interface Catalog extends UICatalog {
  instances: (Instance & { version?: string; pid: number })[];
  available: { id: string; title: string; versions: string[] }[]; hostPID: number; hostIdentity: string;
  operation?: { id: number; phase: string; error?: string };
}
const emptyCatalog: Catalog = { instances: [], packages: [], available: [], hostPID: 0, hostIdentity: '' };
const base = new URL(document.baseURI);
async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(new URL(path, base), options);
  const data = await response.json();
  if (!response.ok) throw new Error(data.error ?? `HTTP ${response.status}`);
  return data as T;
}
async function requestPlugin<T>(instance: Instance, path: string, options?: RequestInit): Promise<T> {
  const pathname = path.split('?')[0] ?? '';
  if (!pathname || pathname.split('/').some(part => !/^[a-zA-Z0-9_-]+$/.test(part))) throw new Error('Invalid relative API path');
  const headers = new Headers(options?.headers);
  headers.set('X-Gordis-Generation', String(instance.generation));
  return request<T>(`api/extensions/${encodeURIComponent(instance.id)}/${path}`, { ...options, headers });
}
function assetURL(path: string): string {
  const url = new URL(path, base);
  if (url.origin !== base.origin || !url.pathname.startsWith(`${base.pathname}assets/plugins/`)) throw new Error('Invalid plugin asset URL');
  return url.href;
}
function loadStyle(path: string) {
  const link = document.createElement('link');
  link.rel = 'stylesheet'; link.href = assetURL(path); link.dataset.pluginStyle = '';
  let timer: ReturnType<typeof setTimeout>;
  const ready = new Promise<void>((resolve, reject) => {
    link.onload = () => { clearTimeout(timer); resolve(); };
    link.onerror = () => { clearTimeout(timer); reject(new Error(`Stylesheet failed: ${path}`)); };
    timer = setTimeout(() => reject(new Error(`Stylesheet timed out: ${path}`)), 10000);
    document.head.append(link);
  });
  return { ready, dispose: () => { clearTimeout(timer); link.remove(); } };
}

class Boundary extends Component<{ children: ReactNode; title: string }, { error: string }> {
  state = { error: '' };
  static getDerivedStateFromError(error: unknown) { return { error: message(error) }; }
  render() {
    return this.state.error ? <p className="error-panel" role="alert">{this.props.title} 无法显示：{this.state.error}</p> : this.props.children;
  }
}
function Extension({ plugin, contribution, instances, instance }: {
  plugin: LoadedPlugin; contribution: Contribution; instances: Instance[]; instance?: Instance;
}) {
  const value = useMemo(() => ({ instances: instances.filter(i => i.packageID === plugin.manifest.id), request: requestPlugin }), [instances, plugin.manifest.id]);
  const content = contribution.kind === 'page' ? <contribution.component />
    : instance ? <contribution.component instance={instance} /> : null;
  return <Boundary title={contribution.title}><PluginProvider value={value}>{content}</PluginProvider></Boundary>;
}
const stateNames: Record<string, string> = { ready: '运行中', stopped: '已停用', starting: '启动中', stopping: '停止中', failed: '失败', registered: '未启动' };

function App() {
  const [catalog, setCatalog] = useState<Catalog>(emptyCatalog);
  const [loaded, setLoaded] = useState<LoadedPlugin[]>([]);
  const [errors, setErrors] = useState<[string, string][]>([]);
  const [notice, setNotice] = useState('');
  const [connected, setConnected] = useState(false);
  const [changing, setChanging] = useState(false);
  const [pathname, setPathname] = useState(location.pathname);
  const [selectedID, setSelectedID] = useState('');
  const manager = useMemo(() => new PluginManager(
    entry => import(assetURL(entry)) as Promise<PluginModule>, loadStyle,
    id => flushSync(() => setLoaded(current => current.filter(p => p.manifest.id !== id))),
  ), []);

  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    const refresh = async () => {
      try {
        const next = await request<Catalog>('api/plugins', { signal: controller.signal });
        await manager.reconcile(next.packages);
        if (disposed) return;
        setCatalog(current => JSON.stringify(current) === JSON.stringify(next) ? current : next);
        setLoaded([...manager.loaded.values()]); setErrors([...manager.errors]); setConnected(true);
      } catch (error) { if (!disposed) { setConnected(false); setNotice(message(error)); } }
      if (!disposed) timer = setTimeout(() => void refresh(), 750);
    };
    void refresh();
    return () => { disposed = true; controller.abort(); clearTimeout(timer); void manager.reconcile([]); };
  }, [manager]);
  useEffect(() => {
    const pop = () => setPathname(location.pathname);
    addEventListener('popstate', pop); return () => removeEventListener('popstate', pop);
  }, []);

  const pages = loaded.flatMap(plugin => plugin.contributions.filter(c => c.kind === 'page').map(contribution => ({
    plugin, contribution, href: `${base.pathname}extensions/${plugin.manifest.id}/${contribution.id}`,
  })));
  const page = pages.find(p => p.href === pathname);
  useEffect(() => {
    if (connected && pathname !== base.pathname && !page) { history.replaceState(null, '', base.pathname); setPathname(base.pathname); }
  }, [connected, pathname, page]);
  const selected = catalog.instances.find(i => i.id === selectedID) ?? catalog.instances[0];
  const panels = selected?.state === 'ready' ? loaded.filter(p => p.manifest.id === selected.packageID)
    .flatMap(plugin => plugin.contributions.filter(c => c.kind === 'instance-panel').map(contribution => ({ plugin, contribution }))) : [];
  const navigate = (href: string) => { history.pushState(null, '', href); setPathname(href); };
  async function toggle(instance: Instance) {
    if (changing) return;
    setChanging(true); setNotice('');
    try { await request(`api/plugins/${instance.id}/${instance.state === 'ready' ? 'disable' : 'enable'}`, { method: 'POST' }); }
    catch (error) { setNotice(message(error)); }
    finally { setChanging(false); }
  }
  async function manage(action: 'load' | 'uninstall', packageID: string, version?: string) {
    if (changing) return;
    setChanging(true); setNotice('');
    try { await request(`api/package/${action}`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ packageID, version }),
    }); } catch (error) { setNotice(message(error)); }
    finally { setChanging(false); }
  }
  const operationBusy = catalog.operation && !['completed', 'failed'].includes(catalog.operation.phase);
  const busy = changing || !!operationBusy;
  const link = (href: string, title: string) => <a key={href} href={href} className={pathname === href ? 'active' : ''}
    onClick={event => { event.preventDefault(); navigate(href); }}>{title}</a>;

  return <>
    <aside className="sidebar">
      <a className="brand" href={base.pathname} onClick={e => { e.preventDefault(); navigate(base.pathname); }}>gordis<span className="brand-dot">.</span></a>
      <div className="workspace-label">APPLICATION PLUGINS <span>01</span></div>
      <nav aria-label="主导航">{link(base.pathname, '概览')}<p className="nav-label">扩展页面</p>
        {pages.map(p => link(p.href, p.contribution.title))}
      </nav>
      <div className="sidebar-footer"><span className="status-dot" />应用后端 + Host UI<span className="version">UI v2</span></div>
    </aside>
    <main>
      <header><span>工作区 / {page?.contribution.title ?? '概览'}</span><span id="connection" role="status">{connected ? '● 已连接' : '连接中'}</span></header>
      {notice && <p className="error-panel" role="alert">{notice}</p>}
      {errors.map(([id, error]) => <div className="error-panel" key={id} role="alert">{id} 界面：{error} <button onClick={() => manager.retry(id)}>重试加载</button></div>)}
      {page ? <section className="extension-page"><Extension key={`${page.plugin.manifest.entry}:${page.contribution.id}`}
        plugin={page.plugin} contribution={page.contribution} instances={catalog.instances} /></section> : <>
        <div className="page-heading"><div><p className="eyebrow">APPLICATION PLUGIN WORKSPACE</p><h1>应用插件工作区</h1>
          <p className="muted">晚安装自带后端与 UI 的应用插件。</p></div><span className="number-label">{loaded.length} 个 UI 包</span></div>
        {catalog.available.map(pkg => {
          const instances = catalog.instances.filter(i => i.packageID === pkg.id);
          return <section key={pkg.id} className="package-controls" aria-label={`${pkg.title}插件包管理`}>
            <div><h2>{pkg.title}</h2><p className="muted small">{instances.length ? `当前版本 ${instances[0]?.version}` : '尚未加载插件包'}</p></div>
            <div className="package-actions">{pkg.versions.map(version => <button key={version}
              className={instances.length ? '' : 'button-primary'} disabled={busy}
              onClick={() => void manage('load', pkg.id, version)}>{instances.length ? '切换到' : '加载'} v{version}</button>)}
              {!!instances.length && <button disabled={busy} onClick={() => void manage('uninstall', pkg.id)}>卸载插件包</button>}
            </div>
          </section>;
        })}
        {!catalog.available.length && <p className="empty-state">尚无可用插件包。构建完成后会自动出现在这里。</p>}
        <div className="section-heading"><h2>后端实例</h2><span className="muted small">{catalog.instances.filter(i => i.state === 'ready').length} / {catalog.instances.length} 运行中</span></div>
        <div className="table-scroll"><table><thead><tr><th>实例</th><th>版本 / 进程</th><th>状态</th><th>激活代次</th><th>操作</th></tr></thead><tbody>
          {catalog.instances.map(instance => <tr key={instance.id}>
            <td><button className="instance-link" onClick={() => setSelectedID(instance.id)} aria-label={`查看 ${instance.id}`}>{instance.title}</button><small>{instance.id}</small></td>
            <td>{instance.version}<small>{instance.pid ? `PID ${instance.pid}` : '进程未运行'}</small></td><td title={instance.lastError}><span className={`state ${instance.state === 'ready' ? 'ready' : ''}`}>{stateNames[instance.state] ?? instance.state}</span></td>
            <td className="mono">{String(instance.generation).padStart(2, '0')}</td>
            <td><button disabled={busy || ['starting', 'stopping'].includes(instance.state)} aria-label={`${instance.state === 'ready' ? '停用' : '启用'} ${instance.id}`}
              onClick={() => void toggle(instance)}>{instance.state === 'ready' ? '停用' : '启用'}</button></td>
          </tr>)}
        </tbody></table></div>
        {!catalog.instances.length && <p className="empty-state">选择插件包及版本，启动包中声明的实例。</p>}
        {catalog.operation && <p className="operation-status" role="status">操作 #{catalog.operation.id} · {catalog.operation.phase}
          {catalog.operation.error && <span> · {catalog.operation.error}</span>}</p>}
        {selected && <section className="instance-details" aria-label="实例详情">
          <div className="section-heading"><h2>实例详情 · {selected.title}</h2><span className="muted small">扩展面板</span></div>
          {panels.length ? panels.map(({ plugin, contribution }) => <Extension key={`${plugin.manifest.entry}:${contribution.id}:${selected.id}:${selected.generation}`}
            plugin={plugin} contribution={contribution} instances={catalog.instances} instance={selected} />)
            : <p className="muted small">该实例暂无活动扩展面板。</p>}
        </section>}
        <div className="help-line"><span>↳</span><p>点击实例查看详情。停用单个实例时，共享页面保留；全部停用后，该包的页面与面板一起撤销。</p></div>
      </>}
      <footer>GORDIS / APPLICATION PLUGIN<span>宿主 PID {catalog.hostPID || '—'} · {catalog.hostIdentity || '连接中'}</span></footer>
    </main>
  </>;
}
createRoot(document.getElementById('root')!).render(<App />);
