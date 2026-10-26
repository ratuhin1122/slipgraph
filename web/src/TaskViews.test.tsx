import { render, screen, fireEvent } from '@testing-library/react';
import { expect, test, vi } from 'vitest';
import { Kanban, Timeline } from './TaskViews';
import type { Task, TaskImpact } from './api/client';
const a: Task = { id: 'a', project_id: 'p', title: 'Design', description: '', status: 'todo', owner_id: null, start: '2030-01-01T00:00:00Z', due: '2030-01-02T00:00:00Z', estimate_minutes: 1440, milestone: false, version: 1, reported_delay_days: 1, completed_at: null, forecast_finish_at: null };
const b: Task = { ...a, id: 'b', title: 'Launch', milestone: true, due: '2030-01-03T00:00:00Z' };
const impact: TaskImpact = { task_id: 'a', baseline_start: a.start, baseline_finish: a.due, earliest_start: a.start, earliest_finish: b.due, slip_minutes: 1440, slack_consumed_minutes: 0, remaining_slack_minutes: -1440, milestone: false, milestone_slip_minutes: 0, critical: true, affected: true, risk: 'high' };
test('timeline draws dependency arrows, highlights risk, and opens a task by keyboard', () => {
  const select = vi.fn(); const { container } = render(<Timeline tasks={[a, b]} impacts={new Map([['a', impact]])} dependencies={[{ task_id: 'b', blocker_id: 'a' }]} launchDue={b.due} canWrite={false} onMove={vi.fn()} onSelect={select} />);
  expect(screen.getByRole('img', { name: 'Task timeline with finish-to-start dependency arrows' })).toBeInTheDocument(); expect(container.querySelectorAll('path[marker-end]')).toHaveLength(1); expect(container.querySelector('rect.fill-rose-500')).not.toBeNull(); fireEvent.keyDown(screen.getByRole('button', { name: 'Open Design' }), { key: 'Enter' }); expect(select).toHaveBeenCalledWith(a);
});
test('a viewer cannot move tasks between Kanban columns', () => { const move = vi.fn(); render(<Kanban tasks={[a]} impacts={new Map([['a', impact]])} canWrite={false} onMove={move} onSelect={vi.fn()} />); const card = screen.getByRole('button', { name: /Design/ }); expect(card).toHaveAttribute('draggable', 'false'); fireEvent.drop(screen.getByRole('region', { name: 'Done' }), { dataTransfer: { getData: () => 'a' } }); expect(move).not.toHaveBeenCalled(); expect(screen.getByText('high risk · critical')).toBeInTheDocument(); });
