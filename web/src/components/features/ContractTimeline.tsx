import { Badge, type BadgeTone } from '@/components/ui/Badge';
import { formatDateTime, formatMoney } from '@/lib/format';
import { cn } from '@/lib/utils';
import type { ContractEvent, ContractEventKind, Currency } from '@/lib/types';

const kindMeta: Record<ContractEventKind, { tone: BadgeTone; dot: string }> = {
  created: { tone: 'neutral', dot: 'bg-slate-400' },
  activated: { tone: 'success', dot: 'bg-success' },
  milestone_funded: { tone: 'info', dot: 'bg-sky-500' },
  milestone_submitted: { tone: 'brand', dot: 'bg-brand-500' },
  milestone_approved: { tone: 'success', dot: 'bg-success' },
  escrow_released: { tone: 'success', dot: 'bg-success' },
  paused: { tone: 'warning', dot: 'bg-warning' },
  completed: { tone: 'success', dot: 'bg-success' },
  disputed: { tone: 'danger', dot: 'bg-danger' },
  message: { tone: 'neutral', dot: 'bg-slate-300' },
};

export interface ContractTimelineProps {
  events: ContractEvent[];
  currency?: Currency;
}

/** Vertical activity timeline for the contract detail view. */
export function ContractTimeline({ events, currency = 'USD' }: ContractTimelineProps) {
  if (events.length === 0) {
    return <p className="text-sm text-muted">No activity yet.</p>;
  }

  return (
    <ol className="relative space-y-6 border-l border-slate-200 pl-6">
      {events.map((event) => {
        const meta = kindMeta[event.kind];
        return (
          <li key={event.id} className="relative">
            <span
              className={cn(
                'absolute -left-[27px] top-1 h-3 w-3 rounded-full ring-4 ring-white',
                meta.dot,
              )}
              aria-hidden
            />
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={meta.tone}>{event.kind.replace(/_/g, ' ')}</Badge>
              <span className="text-xs text-muted">{formatDateTime(event.at)}</span>
              {event.actor_name && (
                <span className="text-xs text-muted">· {event.actor_name}</span>
              )}
            </div>
            <p className="mt-1 text-sm text-slate-800">{event.summary}</p>
            {typeof event.amount_cents === 'number' && (
              <p className="mt-0.5 text-sm font-semibold text-slate-900">
                {formatMoney(event.amount_cents, currency)}
              </p>
            )}
          </li>
        );
      })}
    </ol>
  );
}
