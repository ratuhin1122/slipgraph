import { useQuery } from '@tanstack/react-query';
import { Link, Route, Routes } from 'react-router-dom';
import { getHealth } from './api/client';

function Home() {
  const health = useQuery({ queryKey: ['health'], queryFn: getHealth, refetchInterval: 30_000 });
  return <main className="mx-auto max-w-3xl px-8 py-20">
    <p className="text-sm font-semibold uppercase tracking-widest text-emerald-400">Slipgraph</p>
    <h1 className="mt-5 text-5xl font-semibold tracking-tight">A clearer path for your team.</h1>
    <p className="mt-6 text-lg text-slate-300">Understand downstream risk. Review proposed changes. Keep people in control.</p>
    <section aria-label="System status" className="mt-12 rounded-xl border border-slate-700 bg-slate-900 p-6">
      <h2 className="font-medium">Workspace foundation</h2>
      <p role="status" className="mt-2 text-slate-300">{health.isPending ? 'Checking services…' : health.isError ? 'Services unavailable' : health.data.status === 'ok' ? 'All services ready' : 'Services unavailable'}</p>
      <p className="mt-4 text-sm text-slate-400">This scaffold exposes service health. Team features will follow.</p>
    </section>
  </main>;
}
export default function App() {
  return <Routes><Route path="/" element={<Home />} /><Route path="*" element={<main className="p-12"><h1>Page not found</h1><Link to="/">Back to Slipgraph</Link></main>} /></Routes>;
}
