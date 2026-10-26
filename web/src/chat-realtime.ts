import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useSession } from './auth-context';
export function reconnectDelay(attempt: number, random = Math.random()): number { return Math.min(30_000, 500 * 2 ** Math.min(attempt, 6)) + random * 300; }
export function useChatRealtime(channelId: string) {
  const session = useSession(); const cache = useQueryClient(); const socket = useRef<WebSocket | null>(null); const [connected, setConnected] = useState(false);
  useEffect(() => {
    if (!channelId) return; let stopped = false; let attempt = 0; let retry = 0; let heartbeat = 0; const seen = new Set<string>();
    const invalidate = (kind: string) => { void cache.invalidateQueries({ queryKey: [session.workspace_id, kind] }); };
    function connect() {
      if (stopped) return; const url = new URL('/api/realtime', window.location.href); url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
      const ws = new WebSocket(url); socket.current = ws;
      ws.onmessage = event => {
        if (stopped || typeof event.data !== 'string') return;
        let value: unknown; try { value = JSON.parse(event.data); } catch { return; }
        if (!value || typeof value !== 'object' || !('type' in value)) return;
        if (value.type === 'ready') { ws.send(JSON.stringify({ type: 'subscribe', channel_id: channelId })); return; }
        if (value.type === 'subscribed') { attempt = 0; setConnected(true); invalidate('chat'); invalidate('presence'); heartbeat = window.setInterval(() => { if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'heartbeat' })); }, 25_000); }
        if (value.type === 'event' && 'event_id' in value && typeof value.event_id === 'string') { if (seen.has(value.event_id)) return; seen.add(value.event_id); if (seen.size > 256) { const first = seen.values().next().value; if (first) seen.delete(first); } invalidate('chat'); invalidate('channels'); }
        if (value.type === 'presence') invalidate('presence');
      };
      ws.onclose = () => { window.clearInterval(heartbeat); if (stopped) return; setConnected(false); retry = window.setTimeout(connect, reconnectDelay(attempt++)); };
      ws.onerror = () => ws.close();
    }
    connect(); return () => { stopped = true; window.clearTimeout(retry); window.clearInterval(heartbeat); socket.current?.close(); socket.current = null; };
  }, [session.id, session.workspace_id, channelId, cache]);
  const lastTyping = useRef(0);
  return { connected, typing: (active: boolean) => { const ws = socket.current; if (!ws || ws.readyState !== WebSocket.OPEN || (active && Date.now() - lastTyping.current < 2500)) return; lastTyping.current = Date.now(); ws.send(JSON.stringify({ type: 'typing', channel_id: channelId, active })); } };
}
