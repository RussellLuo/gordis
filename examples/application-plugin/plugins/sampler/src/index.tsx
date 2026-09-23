import { useEffect, useRef, useState } from 'react';
import { usePlugin } from '@gordis/example-ui';
import type { Instance, PluginContext } from '@gordis/example-ui';
import './style.css';

export const apiVersion = 2;
interface Reading {
  version: string; label: string; instance: string; generation: number;
  sequence: number; value: number; pid?: number;
}

function SamplerReading({ instance }: { instance: Instance }) {
  const { request } = usePlugin();
  const [readings, setReadings] = useState<Reading[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const ready = instance.state === 'ready' && instance.groupReady;
  const latest = readings[readings.length - 1];
  async function sample(sequence: number) {
    controller.current?.abort();
    const current = new AbortController();
    controller.current = current;
    setBusy(true); setError('');
    try {
      const reading = await request<Reading>(instance, 'sample', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sequence }), signal: current.signal,
      });
      if (!current.signal.aborted) setReadings(previous => [...previous.slice(-7), reading]);
    } catch (error) {
      if (!current.signal.aborted) setError(error instanceof Error ? error.message : String(error));
    } finally { if (!current.signal.aborted) setBusy(false); }
  }
  useEffect(() => {
    controller.current?.abort();
    setReadings([]);
    setError('');
    setBusy(false);
    return () => controller.current?.abort();
  }, [instance.id, instance.generation, ready]);
  return <article className="sampler-reading" aria-label={`${instance.title} 采样`}>
    <div className="sampler-heading"><div><h2>{instance.title}</h2>
      <p className="muted small">第 {instance.generation} 代 · {ready ? '已连接' : '已停用'}</p></div>
      <button disabled={!ready || busy} onClick={() => void sample((latest?.sequence ?? 6) + 1)}>
        {busy ? '采样中…' : '再次采样'}</button>
    </div>
    <div className="sampler-value" aria-live="polite">{ready ? latest?.value.toLocaleString() ?? '—' : '—'}</div>
    {ready && latest ? <>
      <p className="sampler-meta">后端 v{latest.version} {latest.pid !== undefined && <span>PID {latest.pid}</span>}<span>sequence {latest.sequence}</span></p>
      <div className="sampler-history" aria-label="最近采样结果">{readings.map(r =>
        <span key={r.sequence}><small>#{r.sequence}</small>{r.value}</span>)}</div>
    </> : <p className="muted small">{ready ? '尚无采样记录。' : '重新启用后可继续采样。'}</p>}
    {error && <p className="error-panel" role="alert">采样失败：{error}</p>}
  </article>;
}

function SamplerPage({ version }: { version: string }) {
  const { instances } = usePlugin();
  return <section className="sampler-package" data-ui-version={version}>
    <p className="eyebrow">SAMPLER / UI {version}</p><h1>采样器</h1>
    <p className="muted">Alpha、Beta 各有独立进程，共用这个 UI 页面。</p>
    {instances.map(instance => <SamplerReading key={`${instance.id}:${instance.generation}`} instance={instance} />)}
  </section>;
}

export function activate(ctx: PluginContext) {
  ctx.register({ id: 'overview', kind: 'page', title: '采样器', component: () => <SamplerPage version={ctx.manifest.version} /> });
  ctx.register({ id: 'reading', kind: 'instance-panel', title: '采样读数', component: SamplerReading });
}
