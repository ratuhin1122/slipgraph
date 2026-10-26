import DOMPurify from 'dompurify';
import { Link } from 'react-router-dom';
import type { components } from './api/schema.gen';
export type ChatMessage = components['schemas']['ChatMessage'];
function plainText(value: string): string { return DOMPurify.sanitize(value, { ALLOWED_TAGS: [], ALLOWED_ATTR: [], RETURN_DOM_FRAGMENT: true }).textContent ?? ''; }
export function MessageBody({ message }: { message: ChatMessage }) {
  if (message.deleted) return <p className="italic text-slate-500">Message deleted</p>;
  const text = plainText(message.body); const parts = text.split(/(slipgraph:\/\/task\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/gi);
  return <div className="whitespace-pre-wrap break-words">{parts.map((part, index) => { const task = message.task_links.find(item => part.toLowerCase() === 'slipgraph://task/' + item.id.toLowerCase()); return task ? <Link key={index} className="my-2 block rounded-lg border border-emerald-800 bg-emerald-950/40 p-3 text-emerald-200" to={'/projects/' + task.project_id + '?task=' + task.id}><span className="text-xs uppercase">Task · {task.status}</span><strong className="block">{task.title}</strong></Link> : <span key={index}>{part}</span>; })}{message.mentions.map(member => <span key={member.id} className="mr-2 inline-block rounded bg-slate-800 px-2 text-xs text-sky-300">@{member.name}</span>)}</div>;
}
