'use client';

import Link from 'next/link';

import { PageHeader } from '@/components/layout/PageHeader';
import { Badge } from '@/components/ui/Badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Spinner } from '@/components/ui/Skeleton';
import { Table, TBody, TD, TH, THead, TR, EmptyRow } from '@/components/ui/Table';
import { useContracts } from '@/hooks/useContract';
import { useMyProposals } from '@/hooks/useProjects';
import { paymentsApi } from '@/lib/api/payments';
import { queryKeys } from '@/hooks/queryKeys';
import { formatMoney, formatRelative } from '@/lib/format';
import { useQuery } from '@tanstack/react-query';
import type { ProposalStatus } from '@/lib/types';

const proposalTone: Record<ProposalStatus, 'neutral' | 'success' | 'warning' | 'danger' | 'brand'> = {
  submitted: 'neutral',
  shortlisted: 'brand',
  accepted: 'success',
  declined: 'danger',
  withdrawn: 'warning',
};

export default function FreelancerDashboardPage() {
  const contracts = useContracts();
  const proposals = useMyProposals();
  const earnings = useQuery({
    queryKey: queryKeys.payments.summary(),
    queryFn: () => paymentsApi.summary(),
  });

  const activeContracts =
    contracts.data?.items.filter((c) => c.status === 'active').length ?? 0;

  return (
    <div className="space-y-8">
      <PageHeader
        title="Freelancer dashboard"
        description="Your active work, proposals, and earnings."
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatCard
          label="Available balance"
          value={
            earnings.data
              ? formatMoney(earnings.data.available_cents, earnings.data.currency)
              : '—'
          }
          loading={earnings.isLoading}
        />
        <StatCard
          label="In escrow"
          value={
            earnings.data
              ? formatMoney(earnings.data.in_escrow_cents, earnings.data.currency)
              : '—'
          }
          loading={earnings.isLoading}
        />
        <StatCard label="Active contracts" value={String(activeContracts)} loading={contracts.isLoading} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>My proposals</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {proposals.isLoading ? (
            <div className="flex justify-center p-8">
              <Spinner />
            </div>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Submitted</TH>
                  <TH>Bid</TH>
                  <TH>Status</TH>
                  <TH>Connects</TH>
                </TR>
              </THead>
              <TBody>
                {proposals.data?.items.length ? (
                  proposals.data.items.map((p) => (
                    <TR key={p.id}>
                      <TD>{formatRelative(p.created_at)}</TD>
                      <TD>{formatMoney(p.bid_rate_cents)}</TD>
                      <TD>
                        <Badge tone={proposalTone[p.status]}>{p.status}</Badge>
                      </TD>
                      <TD>{p.connects_spent}</TD>
                    </TR>
                  ))
                ) : (
                  <EmptyRow colSpan={4}>
                    No proposals yet.{' '}
                    <Link href="/marketplace" className="text-brand-600 hover:underline">
                      Find work
                    </Link>
                  </EmptyRow>
                )}
              </TBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Active contracts</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {contracts.data?.items
            .filter((c) => c.status === 'active')
            .map((c) => (
              <Link
                key={c.id}
                href={`/contracts/${c.id}`}
                className="flex items-center justify-between rounded-lg border border-slate-200 p-4 hover:bg-slate-50"
              >
                <div>
                  <p className="font-medium text-slate-900">Contract {c.id.slice(0, 8)}</p>
                  <p className="text-xs text-muted capitalize">{c.type} · {c.status}</p>
                </div>
                <Badge tone="info">{formatMoney(c.escrow_balance_cents, c.currency)} in escrow</Badge>
              </Link>
            )) ?? null}
          {!contracts.isLoading &&
            !contracts.data?.items.some((c) => c.status === 'active') && (
              <p className="text-sm text-muted">No active contracts.</p>
            )}
        </CardContent>
      </Card>
    </div>
  );
}

function StatCard({
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
      <p className="mt-1 text-2xl font-bold text-slate-900">{loading ? '…' : value}</p>
    </Card>
  );
}
