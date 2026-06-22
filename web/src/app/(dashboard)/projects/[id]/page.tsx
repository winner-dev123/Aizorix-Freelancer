'use client';

import Link from 'next/link';
import { useParams } from 'next/navigation';

import { PageHeader } from '@/components/layout/PageHeader';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { useProject } from '@/hooks/useProjects';
import { formatBudgetRange, formatRelative } from '@/lib/format';

export default function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { data: project, isLoading, isError } = useProject(id);

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-96" />
        <Skeleton className="h-48 w-full rounded-2xl" />
      </div>
    );
  }

  if (isError || !project) {
    return (
      <p className="rounded-lg bg-red-50 p-4 text-sm text-red-700">
        This project could not be loaded or no longer exists.
      </p>
    );
  }

  const hourly = project.budget_type === 'hourly';

  return (
    <div className="mx-auto max-w-4xl">
      <PageHeader
        title={project.title}
        description={project.created_at ? `Posted ${formatRelative(project.created_at)}` : undefined}
        actions={
          <Link href={`/proposals/new?project=${project.id}`}>
            <Button>Submit a proposal</Button>
          </Link>
        }
      />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>Project details</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="whitespace-pre-wrap text-sm leading-relaxed text-slate-700">
              {project.description}
            </p>
            <div className="mt-6 flex flex-wrap gap-1.5">
              {project.skills.map((s) => (
                <Badge key={s}>{s}</Badge>
              ))}
            </div>
          </CardContent>
        </Card>

        <Card className="h-fit">
          <CardContent className="space-y-4">
            <div>
              <p className="text-sm text-muted">Budget</p>
              <p className="text-lg font-semibold text-slate-900">
                {formatBudgetRange(
                  project.budget_min_cents,
                  project.budget_max_cents,
                  project.currency,
                  hourly,
                )}
              </p>
            </div>
            <div>
              <p className="text-sm text-muted">Type</p>
              <Badge tone={hourly ? 'info' : 'brand'}>{hourly ? 'Hourly' : 'Fixed price'}</Badge>
            </div>
            <div>
              <p className="text-sm text-muted">Status</p>
              <Badge tone="neutral">{project.status}</Badge>
            </div>
            {typeof project.proposals_count === 'number' && (
              <div>
                <p className="text-sm text-muted">Proposals</p>
                <p className="font-medium text-slate-900">{project.proposals_count}</p>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
