// Shared runtime contract. Both host and plugins resolve this module through
// the same import map; a bundled private copy would split React context.
import { createContext, useContext } from 'react';
import type { ComponentType, ReactNode } from 'react';

export const uiAPIVersion = 2;
export interface PackageManifest {
  id: string; version: string; title: string; apiVersion: number;
  activation: 'backend'; entry: string; styles: string[];
}
export interface Instance {
  id: string; packageID: string; title: string; state: string;
  generation: number; groupReady: boolean; lastError?: string;
}
export interface Catalog { instances: Instance[]; packages: PackageManifest[] }
export type Dispose = () => void;
export type Contribution =
  | { id: string; kind: 'page'; title: string; component: ComponentType }
  | { id: string; kind: 'instance-panel'; title: string; component: ComponentType<{ instance: Instance }> };
export interface PluginContext {
  readonly manifest: Readonly<PackageManifest>;
  register(contribution: Contribution): void;
  defer(dispose: Dispose): void;
}
export interface PluginModule {
  apiVersion: number;
  activate(ctx: PluginContext): void | Promise<void>;
}
export interface PluginAPI {
  readonly instances: Instance[];
  request<T>(instance: Instance, path: string, options?: RequestInit): Promise<T>;
}
const Runtime = createContext<PluginAPI | null>(null);
export function PluginProvider({ value, children }: { value: PluginAPI; children: ReactNode }) {
  return <Runtime.Provider value={value}>{children}</Runtime.Provider>;
}
export function usePlugin(): PluginAPI {
  const api = useContext(Runtime);
  if (!api) throw new Error('PluginProvider missing: check the shared UI SDK import');
  return api;
}
