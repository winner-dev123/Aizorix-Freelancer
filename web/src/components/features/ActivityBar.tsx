import { activityTier, formatPercent } from '@/lib/format';
import { cn } from '@/lib/utils';

export interface ActivityBarProps {
  /** 0–100 activity percentage. */
  pct: number;
  /** Show the numeric label beside the bar. */
  showLabel?: boolean;
  className?: string;
}

const tierColor = {
  high: 'bg-success',
  medium: 'bg-warning',
  low: 'bg-danger',
} as const;

/**
 * Horizontal activity meter colored by tier (green ≥60%, amber ≥30%, red below).
 * Used in the screenshot grid and weekly timesheet to surface low-activity work.
 */
export function ActivityBar({ pct, showLabel = true, className }: ActivityBarProps) {
  const clamped = Math.max(0, Math.min(100, pct));
  const tier = activityTier(clamped);
  return (
    <div className={cn('flex items-center gap-2', className)}>
      <div
        className="h-1.5 w-full overflow-hidden rounded-full bg-slate-200"
        role="progressbar"
        aria-valuenow={Math.round(clamped)}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <div
          className={cn('h-full rounded-full transition-all', tierColor[tier])}
          style={{ width: `${clamped}%` }}
        />
      </div>
      {showLabel && (
        <span className="w-9 shrink-0 text-right text-xs font-medium tabular-nums text-slate-600">
          {formatPercent(clamped)}
        </span>
      )}
    </div>
  );
}
