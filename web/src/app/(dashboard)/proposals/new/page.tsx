'use client';

import { useRouter, useSearchParams } from 'next/navigation';
import { Suspense } from 'react';

import { PageHeader } from '@/components/layout/PageHeader';
import { ProposalForm } from '@/components/features/ProposalForm';
import { Card, CardContent } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { useProject, useSubmitProposal } from '@/hooks/useProjects';
import { formatBudgetRange } from '@/lib/format';

function NewProposalForm() {
  const router = useRouter();
  const params = useSearchParams();
  const projectId = params.get('project') ?? '';

  const { data: project, isLoading } = useProject(projectId || undefined);
  const submit = useSubmitProposal();

  if (!projectId) {
    return (
      <p className="rounded-lg bg-amber-50 p-4 text-sm text-amber-800">
        No project selected. Open a project from the marketplace to submit a proposal.
      </p>
    );
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Submit a proposal" description="Make your case and place your bid." />

      <Card>
        <CardContent className="space-y-6">
          {isLoading ? (
            <Skeleton className="h-16 w-full rounded-lg" />
          ) : project ? (
            <div className="rounded-lg bg-slate-50 p-4">
              <p className="font-medium text-slate-900">{project.title}</p>
              <p className="mt-1 text-sm text-muted">
                {formatBudgetRange(
                  project.budget_min_cents,
                  project.budget_max_cents,
                  project.currency,
                  project.budget_type === 'hourly',
                )}
              </p>
            </div>
          ) : null}

          {project && (
            <ProposalForm
              projectId={project.id}
              budgetType={project.budget_type}
              isSubmitting={submit.isPending}
              error={submit.error}
              onSubmit={(input) =>
                submit.mutate(input, {
                  onSuccess: () => router.push('/freelancer'),
                })
              }
            />
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export default function NewProposalPage() {
  return (
    <Suspense fallback={null}>
      <NewProposalForm />
    </Suspense>
  );
}
