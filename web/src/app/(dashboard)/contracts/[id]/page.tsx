'use client';

import Link from 'next/link';
import { useParams } from 'next/navigation';

import { PageHeader } from '@/components/layout/PageHeader';
import { ContractTimeline } from '@/components/features/ContractTimeline';
import { Badge, type BadgeTone } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  useApproveMilestone,
  useContract,
  useContractEscrow,
  useContractTimeline,
} from '@/hooks/useContract';
import { useAuth } from '@/hooks/useAuth';
import { formatDate, formatMoney } from '@/lib/format';
import type { ContractStatus, MilestoneStatus } from '@/lib/types';

const statusTone: Record<ContractStatus, BadgeTone> = {
  pending_funding: 'neutral',
  active: 'success',
  paused: 'warning',
  completed: 'info',
  disputed: 'danger',
  cancelled: 'neutral',
};

const milestoneTone: Record<MilestoneStatus, BadgeTone> = {
  pending: 'neutral',
  funded: 'info',
  submitted: 'brand',
  approved: 'success',
  released: 'success',
  disputed: 'danger',
};

export default function ContractDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { user } = useAuth();
  const contract = useContract(id);
  const timeline = useContractTimeline(id);
  const escrow = useContractEscrow(id);
  const approve = useApproveMilestone(id);

  const isClient = user?.role === 'client';

  if (contract.isLoading) {
    return <Skeleton className="h-96 w-full rounded-2xl" />;
  }
  if (contract.isError || !contract.data) {
    return (
      <p className="rounded-lg bg-red-50 p-4 text-sm text-red-700">Contract not found.</p>
    );
  }

  const c = contract.data;
  const milestones = c.milestones ?? [];

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <PageHeader
        title={`Contract ${c.id.slice(0, 8)}`}
        description={`${c.budget_type} · ${c.currency}`}
        actions={
          <div className="flex items-center gap-2">
            <Badge tone={statusTone[c.status]} dot>
              {c.status}
            </Badge>
            {c.budget_type === 'hourly' && (
              <Link href={`/contracts/${c.id}/time`}>
                <Button variant="outline">Timesheet &amp; screenshots</Button>
              </Link>
            )}
          </div>
        }
      />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="space-y-6 lg:col-span-2">
          <Card>
            <CardHeader>
              <CardTitle>Milestones</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              {milestones.length === 0 && (
                <p className="text-sm text-muted">
                  {c.budget_type === 'hourly'
                    ? 'Hourly contract — billed weekly from verified time.'
                    : 'No milestones defined.'}
                </p>
              )}
              {milestones.map((m) => (
                <div
                  key={m.seq}
                  className="flex items-center justify-between rounded-lg border border-slate-200 p-4"
                >
                  <div>
                    <p className="font-medium text-slate-900">
                      {m.seq}. {m.title}
                    </p>
                    {m.due_at && (
                      <p className="text-xs text-muted">Due {formatDate(m.due_at)}</p>
                    )}
                  </div>
                  <div className="flex items-center gap-3">
                    <span className="font-semibold text-slate-900">
                      {formatMoney(m.amount_cents, c.currency)}
                    </span>
                    <Badge tone={milestoneTone[m.status]}>{m.status}</Badge>
                    {isClient && m.status === 'submitted' && (
                      <Button
                        size="sm"
                        isLoading={approve.isPending}
                        onClick={() => approve.mutate(m.id)}
                      >
                        Approve &amp; release
                      </Button>
                    )}
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Activity</CardTitle>
            </CardHeader>
            <CardContent>
              {timeline.isLoading ? (
                <Skeleton className="h-40 w-full" />
              ) : (
                <ContractTimeline events={timeline.data ?? []} />
              )}
            </CardContent>
          </Card>
        </div>

        <Card className="h-fit">
          <CardContent className="space-y-4">
            <div>
              <p className="text-sm text-muted">Escrow balance</p>
              <p className="text-xl font-bold text-slate-900">
                {escrow.data
                  ? formatMoney(escrow.data.held_cents, c.currency)
                  : escrow.isLoading
                    ? '…'
                    : '—'}
              </p>
            </div>
            {c.budget_type === 'hourly' && c.hourly_rate_cents != null && (
              <div>
                <p className="text-sm text-muted">Hourly rate</p>
                <p className="font-medium text-slate-900">
                  {formatMoney(c.hourly_rate_cents, c.currency)} /hr
                </p>
              </div>
            )}
            {c.weekly_hour_limit != null && (
              <div>
                <p className="text-sm text-muted">Weekly limit</p>
                <p className="font-medium text-slate-900">{c.weekly_hour_limit} h</p>
              </div>
            )}
            <div>
              <p className="text-sm text-muted">Started</p>
              <p className="font-medium text-slate-900">
                {c.started_at ? formatDate(c.started_at) : 'Not yet active'}
              </p>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
