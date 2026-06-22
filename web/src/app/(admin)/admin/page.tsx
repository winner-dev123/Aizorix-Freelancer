'use client';

import { PageHeader } from '@/components/layout/PageHeader';
import { Badge } from '@/components/ui/Badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/Table';

/**
 * Back-office overview. In a full build these tiles would be backed by the
 * admin/analytics services; placeholder shapes match those endpoints.
 */
const metrics = [
  { label: 'Active contracts', value: '1,284' },
  { label: 'Open disputes', value: '17' },
  { label: 'Fraud cases', value: '6' },
  { label: 'GMV (30d)', value: '$2.41M' },
];

const fraudQueue = [
  { id: 'fc_8821', contract: 'c_19af…', reason: 'Activity anomaly', risk: 'high' as const },
  { id: 'fc_8822', contract: 'c_44bd…', reason: 'Duplicate screenshots', risk: 'medium' as const },
  { id: 'fc_8825', contract: 'c_7c01…', reason: 'Impossible travel', risk: 'high' as const },
];

const riskTone = { high: 'danger', medium: 'warning', low: 'neutral' } as const;

export default function AdminPage() {
  return (
    <div className="space-y-8">
      <PageHeader title="Admin overview" description="Platform health, disputes, and fraud." />

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {metrics.map((m) => (
          <Card key={m.label} className="p-5">
            <p className="text-sm text-muted">{m.label}</p>
            <p className="mt-1 text-2xl font-bold text-slate-900">{m.value}</p>
          </Card>
        ))}
      </div>

      <Card id="fraud">
        <CardHeader>
          <CardTitle>Fraud queue</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <THead>
              <TR>
                <TH>Case</TH>
                <TH>Contract</TH>
                <TH>Reason</TH>
                <TH>Risk</TH>
              </TR>
            </THead>
            <TBody>
              {fraudQueue.map((row) => (
                <TR key={row.id}>
                  <TD className="font-mono text-xs">{row.id}</TD>
                  <TD className="font-mono text-xs">{row.contract}</TD>
                  <TD>{row.reason}</TD>
                  <TD>
                    <Badge tone={riskTone[row.risk]}>{row.risk}</Badge>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}
