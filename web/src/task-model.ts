import type { Task } from './api/client';
export const statuses = ['todo', 'in_progress', 'blocked', 'done', 'cancelled'] as const;
export const statusName: Record<Task['status'], string> = { todo: 'To do', in_progress: 'In progress', blocked: 'Blocked', done: 'Done', cancelled: 'Cancelled' };
