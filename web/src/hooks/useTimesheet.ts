'use client';

import { useQuery } from '@tanstack/react-query';

import { trackingApi } from '@/lib/api/tracking';
import type { UUID } from '@/lib/types';

import { queryKeys } from './queryKeys';

/** Current ISO-week Monday as a date-only string. */
export function currentWeekStart(date = new Date()): string {
  const d = new Date(date);
  const day = (d.getDay() + 6) % 7; // 0 = Monday
  d.setDate(d.getDate() - day);
  d.setHours(0, 0, 0, 0);
  return d.toISOString().slice(0, 10);
}

/** Aggregated billable timesheet for one ISO week of a contract. */
export function useTimesheetWeek(contractId: UUID | undefined, weekStart: string) {
  return useQuery({
    queryKey: queryKeys.timesheets.week(contractId ?? '', weekStart),
    queryFn: () => trackingApi.weeklyTimesheet(contractId as UUID, weekStart),
    enabled: Boolean(contractId),
  });
}

/** All weekly summaries for a contract (timesheet index). */
export function useTimesheets(contractId: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.timesheets.all(contractId ?? ''),
    queryFn: () => trackingApi.timesheets(contractId as UUID),
    enabled: Boolean(contractId),
  });
}
