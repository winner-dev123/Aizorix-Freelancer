'use client';

import { useState } from 'react';
import Link from 'next/link';
import { useParams } from 'next/navigation';

import { PageHeader } from '@/components/layout/PageHeader';
import { ActivityBar } from '@/components/features/ActivityBar';
import { ScreenshotGrid } from '@/components/features/ScreenshotGrid';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { useContractScreenshots } from '@/hooks/useScreenshots';
import { currentWeekStart, useTimesheetWeek } from '@/hooks/useTimesheet';
import { formatDate, formatDuration, formatMoney, formatPercent } from '@/lib/format';

/** Shift an ISO date-only string by a number of weeks. */
function shiftWeek(weekStart: string, deltaWeeks: number): string {
  const d = new Date(weekStart);
  d.setDate(d.getDate() + deltaWeeks * 7);
  return d.toISOString().slice(0, 10);
}

export default function ContractTimePage() {
  const { id } = useParams<{ id: string }>();
  const [weekStart, setWeekStart] = useState(() => currentWeekStart());

  const timesheet = useTimesheetWeek(id, weekStart);
  const screenshots = useContractScreenshots(id, { from: weekStart, to: shiftWeek(weekStart, 1) });

  const isCurrentWeek = weekStart === currentWeekStart();

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <PageHeader
        title="Timesheet & screenshots"
        description="Verified hourly work for this contract."
        actions={
          <Link href={`/contracts/${id}`}>
            <Button variant="ghost">Back to contract</Button>
          </Link>
        }
      />

      {/* Week navigator */}
      <div className="flex items-center justify-between">
        <Button variant="outline" size="sm" onClick={() => setWeekStart((w) => shiftWeek(w, -1))}>
          ← Previous week
        </Button>
        <span className="text-sm font-medium text-slate-700">
          Week of {formatDate(weekStart)}
        </span>
        <Button
          variant="outline"
          size="sm"
          disabled={isCurrentWeek}
          onClick={() => setWeekStart((w) => shiftWeek(w, 1))}
        >
          Next week →
        </Button>
      </div>

      {/* Weekly summary */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <SummaryCard
          label="Tracked time"
          value={timesheet.data ? formatDuration(timesheet.data.total_seconds) : '—'}
          loading={timesheet.isLoading}
        />
        <SummaryCard
          label="Billable amount"
          value={
            timesheet.data
              ? formatMoney(timesheet.data.amount_cents, timesheet.data.currency)
              : '—'
          }
          loading={timesheet.isLoading}
        />
        <Card className="p-5">
          <p className="text-sm text-muted">Avg. activity</p>
          {timesheet.isLoading ? (
            <Skeleton className="mt-2 h-6 w-24" />
          ) : (
            <div className="mt-2 flex items-center gap-3">
              <span className="text-2xl font-bold text-slate-900">
                {formatPercent(timesheet.data?.avg_activity_pct ?? 0)}
              </span>
              <Badge tone={timesheet.data?.status === 'billed' ? 'success' : 'neutral'}>
                {timesheet.data?.status ?? 'open'}
              </Badge>
            </div>
          )}
        </Card>
      </div>

      {/* Interval breakdown */}
      <Card>
        <CardHeader>
          <CardTitle>Activity by interval</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {timesheet.isLoading && <Skeleton className="h-32 w-full" />}
          {timesheet.data?.intervals.length === 0 && (
            <p className="text-sm text-muted">No tracked intervals this week.</p>
          )}
          {timesheet.data?.intervals.map((iv) => (
            <div key={iv.start} className="flex items-center gap-4">
              <span className="w-28 shrink-0 text-xs text-muted">
                {new Date(iv.start).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
              </span>
              <ActivityBar pct={iv.activity_pct} className="flex-1" />
              {iv.flagged && <Badge tone="danger">flagged</Badge>}
            </div>
          ))}
        </CardContent>
      </Card>

      {/* Screenshot review grid */}
      <Card>
        <CardHeader>
          <CardTitle>Screenshots</CardTitle>
        </CardHeader>
        <CardContent>
          <ScreenshotGrid
            screenshots={screenshots.data ?? []}
            isLoading={screenshots.isLoading}
          />
        </CardContent>
      </Card>
    </div>
  );
}

function SummaryCard({
  label,
  value,
  loading,
}: {
  label: string;
  value: string;
  loading?: boolean;
}) {
  return (
    <Card className="p-5">
      <p className="text-sm text-muted">{label}</p>
      {loading ? (
        <Skeleton className="mt-2 h-7 w-28" />
      ) : (
        <p className="mt-1 text-2xl font-bold text-slate-900">{value}</p>
      )}
    </Card>
  );
}
