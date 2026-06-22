'use client';

import { useQuery } from '@tanstack/react-query';

import { PageHeader } from '@/components/layout/PageHeader';
import { Badge, type BadgeTone } from '@/components/ui/Badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { Table, TBody, TD, TH, THead, TR, EmptyRow } from '@/components/ui/Table';
import { queryKeys } from '@/hooks/queryKeys';
import { paymentsApi } from '@/lib/api/payments';
import { formatDateTime, formatMoney } from '@/lib/format';
import type { PaymentDirection, PaymentStatus } from '@/lib/types';

const statusTone: Record<PaymentStatus, BadgeTone> = {
  pending: 'neutral',
  processing: 'info',
  succeeded: 'success',
  failed: 'danger',
  reversed: 'warning',
};

const directionLabel: Record<PaymentDirection, string> = {
  charge: 'Charge',
  payout: 'Payout',
  refund: 'Refund',
  fee: 'Fee',
};

export default function PaymentsPage() {
  const summary = useQuery({
    queryKey: queryKeys.payments.summary(),
    queryFn: () => paymentsApi.summary(),
  });
  const tx = useQuery({
    queryKey: queryKeys.payments.transactions(),
    queryFn: () => paymentsApi.transactions(),
  });

  const s = summary.data;

  return (
    <div className="space-y-8">
      <PageHeader title="Payments" description="Balances, escrow, and your transaction ledger." />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Available" value={s && formatMoney(s.available_cents, s.currency)} loading={summary.isLoading} />
        <Stat label="Pending" value={s && formatMoney(s.pending_cents, s.currency)} loading={summary.isLoading} />
        <Stat label="In escrow" value={s && formatMoney(s.in_escrow_cents, s.currency)} loading={summary.isLoading} />
        <Stat label="Lifetime" value={s && formatMoney(s.lifetime_cents, s.currency)} loading={summary.isLoading} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Transactions</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {tx.isLoading ? (
            <div className="p-6">
              <Skeleton className="h-40 w-full" />
            </div>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Date</TH>
                  <TH>Type</TH>
                  <TH>Description</TH>
                  <TH>Status</TH>
                  <TH className="text-right">Amount</TH>
                </TR>
              </THead>
              <TBody>
                {tx.data?.items.length ? (
                  tx.data.items.map((t) => (
                    <TR key={t.id}>
                      <TD className="whitespace-nowrap">{formatDateTime(t.created_at)}</TD>
                      <TD>{directionLabel[t.direction]}</TD>
                      <TD className="text-slate-600">{t.description}</TD>
                      <TD>
                        <Badge tone={statusTone[t.status]}>{t.status}</Badge>
                      </TD>
                      <TD
                        className={`text-right font-semibold ${
                          t.direction === 'payout' ? 'text-success' : 'text-slate-900'
                        }`}
                      >
                        {t.direction === 'charge' || t.direction === 'fee' ? '-' : '+'}
                        {formatMoney(t.amount_cents, t.currency)}
                      </TD>
                    </TR>
                  ))
                ) : (
                  <EmptyRow colSpan={5}>No transactions yet.</EmptyRow>
                )}
              </TBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Stat({
  label,
  value,
  loading,
}: {
  label: string;
  value: string | undefined | false;
  loading?: boolean;
}) {
  return (
    <Card className="p-5">
      <p className="text-sm text-muted">{label}</p>
      {loading ? (
        <Skeleton className="mt-2 h-7 w-24" />
      ) : (
        <p className="mt-1 text-2xl font-bold text-slate-900">{value || '—'}</p>
      )}
    </Card>
  );
}
