import { Badge } from '@/components/ui/Badge';
import { formatDateTime } from '@/lib/format';
import type { ContractEvent } from '@/lib/types';

export interface ContractTimelineProps {
  events: ContractEvent[];
}

/** Vertical activity timeline for the contract detail view, rendered from the contract
 *  service's event-sourced transition log (each entry is an event + a status change). */
export function ContractTimeline({ events }: ContractTimelineProps) {
  if (events.length === 0) {
    return <p className="text-sm text-muted">No activity yet.</p>;
  }

  return (
    <ol className="relative space-y-6 border-l border-slate-200 pl-6">
      {events.map((event, i) => (
        <li key={i} className="relative">
          <span
            className="absolute -left-[27px] top-1 h-3 w-3 rounded-full bg-brand-500 ring-4 ring-white"
            aria-hidden
          />
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="neutral">{event.event.replace(/[_.]/g, ' ')}</Badge>
            <span className="text-xs text-muted">{formatDateTime(event.created_at)}</span>
          </div>
          <p className="mt-1 text-sm text-slate-800">
            {event.from_status ? `${event.from_status} → ${event.to_status}` : event.to_status}
          </p>
        </li>
      ))}
    </ol>
  );
}
