'use client';

import { Activity, AlertTriangle, Box, CheckCircle2, ChevronDown, CircleHelp, FileText, GitBranch, LayoutDashboard, RefreshCw, Search, Settings, Sparkles, Wifi } from 'lucide-react';
import { useEffect, useState } from 'react';

type Summary = { events: number; logs: number; metrics: number; traces: number; last_received: string | null };
type Event = { tenant_id: string; received_at: string; event: { type: 'logs' | 'metrics' | 'traces'; timestamp: string; payload: Record<string, unknown> } };
const apiBase = process.env.NEXT_PUBLIC_SIGNAL_API_URL ?? 'http://localhost:8080';
const token = process.env.NEXT_PUBLIC_SIGNAL_TOKEN ?? 'local-only-token';

export default function Dashboard() {
  const [summary, setSummary] = useState<Summary | null>(null);
  const [events, setEvents] = useState<Event[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [lastSync, setLastSync] = useState<Date | null>(null);

  async function loadData() {
    try {
      setError('');
      const headers = { Authorization: `Bearer ${token}` };
      const [summaryResponse, eventsResponse] = await Promise.all([
        fetch(`${apiBase}/v1/summary`, { headers, cache: 'no-store' }),
        fetch(`${apiBase}/v1/telemetry?limit=12`, { headers, cache: 'no-store' }),
      ]);
      if (!summaryResponse.ok || !eventsResponse.ok) throw new Error('Signal intake is not reachable');
      setSummary(await summaryResponse.json());
      setEvents((await eventsResponse.json()).events ?? []);
      setLastSync(new Date());
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Could not load telemetry');
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadData();
    const timer = window.setInterval(() => void loadData(), 5000);
    return () => window.clearInterval(timer);
  }, []);

  return <main className="shell">
    <aside className="sidebar">
      <div className="logo"><span><Sparkles size={16} /></span>signal <small>PREVIEW</small></div>
      <div className="workspace"><div className="workspace-icon">A</div><div><strong>Acme production</strong><small>Workspace</small></div><ChevronDown size={15} /></div>
      <nav><p>Observe</p><NavItem icon={<LayoutDashboard size={16} />} label="Overview" active /><NavItem icon={<Box size={16} />} label="Services" /><NavItem icon={<FileText size={16} />} label="Logs" /><NavItem icon={<GitBranch size={16} />} label="Traces" /><NavItem icon={<Activity size={16} />} label="Metrics" /><p className="manage">Manage</p><NavItem icon={<Settings size={16} />} label="Settings" /></nav>
      <div className="side-bottom"><div className="connection"><span className="live" /> Collector connected <small>OTLP / local intake</small></div><div className="profile"><span>JD</span><div><strong>Jordan Davis</strong><small>Administrator</small></div></div></div>
    </aside>
    <section className="main"><header className="topbar"><div className="crumb">Acme production <span>/</span> Overview</div><div className="top-actions"><button title="Search"><Search size={16} /></button><button title="Help"><CircleHelp size={16} /></button><span className="healthy"><span className="live" /> Intake healthy</span></div></header>
      <div className="content"><div className="heading"><div><div className="eyebrow">Production workspace</div><h1>System overview</h1><p>Live telemetry from your connected infrastructure.</p></div><button className="refresh" onClick={() => void loadData()}><RefreshCw size={15} /> Refresh</button></div>
        {error && <div className="error-banner"><AlertTriangle size={16} /><span>{error}. Start the intake service on port 8080, then refresh.</span></div>}
        <div className="cards"><MetricCard label="Total events" value={summary?.events ?? 0} detail="All signals" icon={<Activity size={17} />} tone="teal" loading={loading} /><MetricCard label="Logs" value={summary?.logs ?? 0} detail="Structured events" icon={<FileText size={17} />} tone="blue" loading={loading} /><MetricCard label="Metrics" value={summary?.metrics ?? 0} detail="Measurements" icon={<Activity size={17} />} tone="gold" loading={loading} /><MetricCard label="Traces" value={summary?.traces ?? 0} detail="Distributed spans" icon={<GitBranch size={17} />} tone="rose" loading={loading} /></div>
        <div className="grid"><section className="panel stream-panel"><div className="panel-header"><div><h2>Recent telemetry</h2><p>Latest events received by Signal</p></div><span className="count">{events.length} shown</span></div>{events.length === 0 && !loading ? <EmptyState /> : <div className="event-list">{events.slice().reverse().map((item, index) => <EventRow key={`${item.received_at}-${index}`} event={item} />)}</div>}</section><section className="panel posture-panel"><div className="panel-header"><div><h2>Platform posture</h2><p>Ingestion pipeline status</p></div><CheckCircle2 className="check" size={18} /></div><div className="posture-list"><Posture label="Collector endpoint" value="Operational" /><Posture label="Tenant authentication" value="Enforced" /><Posture label="Telemetry persistence" value={summary?.events ? 'Receiving data' : 'Waiting for data'} warning={!summary?.events} /></div><div className="protocol"><Wifi size={15} /><div><strong>OTLP enabled</strong><span>HTTP :4318 · gRPC :4317</span></div></div></section></div>
        <footer><span><span className="live" /> {lastSync ? `Updated ${lastSync.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}` : 'Waiting for sync'}</span><span>Retention: local JSONL</span><span className="region">Development · AWS-ready</span></footer>
      </div>
    </section>
  </main>;
}

function NavItem({ icon, label, active = false }: { icon: React.ReactNode; label: string; active?: boolean }) { return <button className={`nav-item ${active ? 'active' : ''}`}>{icon}<span>{label}</span></button>; }
function MetricCard({ label, value, detail, icon, tone, loading }: { label: string; value: number; detail: string; icon: React.ReactNode; tone: string; loading: boolean }) { return <div className="metric-card"><span className={`metric-icon ${tone}`}>{icon}</span><span className="metric-label">{label}</span><strong>{loading ? '—' : value.toLocaleString()}</strong><small>{detail}</small></div>; }
function EventRow({ event }: { event: Event }) { const type = event.event.type; const Icon = type === 'logs' ? FileText : type === 'traces' ? GitBranch : Activity; const payload = JSON.stringify(event.event.payload); return <div className="event-row"><span className={`event-icon ${type}`}><Icon size={14} /></span><div className="event-main"><strong>{type}</strong><span>{payload.length > 92 ? `${payload.slice(0, 92)}...` : payload}</span></div><time>{new Date(event.received_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time></div>; }
function Posture({ label, value, warning = false }: { label: string; value: string; warning?: boolean }) { return <div className="posture-row"><span className={warning ? 'dot warning' : 'dot'} /><div><strong>{label}</strong><small>{value}</small></div><span className={warning ? 'posture-value warning-text' : 'posture-value'}>{warning ? 'Idle' : 'Healthy'}</span></div>; }
function EmptyState() { return <div className="empty"><Activity size={22} /><strong>No telemetry yet</strong><span>Send OTLP logs, metrics, or traces to the collector to see them here.</span></div>; }
