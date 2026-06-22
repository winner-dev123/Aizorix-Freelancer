import { get, post } from '@/lib/api/client';
import type { TimesheetWeek, UUID, WorkSession } from '@/lib/types';

/** Time-tracking service — wraps /v1/tracking. */
export const trackingApi = {
  /** Start a work session (normally called by the desktop tracker). */
  startSession(contractId: UUID, memo?: string): Promise<WorkSession> {
    return post<WorkSession>('/v1/tracking/sessions', {
      contract_id: contractId,
      memo,
    });
  },

  stopSession(sessionId: UUID): Promise<WorkSession> {
    return post<WorkSession>(`/v1/tracking/sessions/${sessionId}/stop`, {});
  },

  /** Aggregated billable timesheet for one ISO week of a contract.
   *  The week-start value is passed under the `billing_week` query key. */
  weeklyTimesheet(contractId: UUID, weekStart: string): Promise<TimesheetWeek> {
    return get<TimesheetWeek>(`/v1/tracking/contracts/${contractId}/timesheet`, {
      params: { billing_week: weekStart },
    });
  },

  /** All weekly summaries for a contract (timesheet index). */
  timesheets(contractId: UUID): Promise<TimesheetWeek[]> {
    return get<TimesheetWeek[]>(`/v1/tracking/contracts/${contractId}/timesheets`);
  },
};
