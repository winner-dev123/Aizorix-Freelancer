'use client';

import { useState } from 'react';

import { PageHeader } from '@/components/layout/PageHeader';
import { ProjectCard } from '@/components/features/ProjectCard';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Skeleton } from '@/components/ui/Skeleton';
import { useProjectSearch } from '@/hooks/useProjects';
import type { BudgetType } from '@/lib/types';

export default function MarketplacePage() {
  const [q, setQ] = useState('');
  const [budgetType, setBudgetType] = useState<BudgetType | undefined>();
  const [submitted, setSubmitted] = useState<{ q?: string; budget_type?: BudgetType }>({});

  const { data, isLoading, isError, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useProjectSearch({ q: submitted.q, budget_type: submitted.budget_type });

  const projects = data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div>
      <PageHeader
        title="Marketplace"
        description="Browse open projects from clients around the world."
      />

      <form
        className="mb-6 flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted({ q: q || undefined, budget_type: budgetType });
        }}
      >
        <div className="min-w-[240px] flex-1">
          <Input
            label="Search"
            placeholder="React, Go, design…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        <div className="flex gap-2">
          {(['hourly', 'fixed'] as const).map((t) => (
            <Button
              key={t}
              type="button"
              variant={budgetType === t ? 'primary' : 'outline'}
              onClick={() => setBudgetType((cur) => (cur === t ? undefined : t))}
            >
              {t === 'hourly' ? 'Hourly' : 'Fixed'}
            </Button>
          ))}
        </div>
        <Button type="submit">Search</Button>
      </form>

      {isLoading && (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-52 w-full rounded-2xl" />
          ))}
        </div>
      )}

      {isError && (
        <p className="rounded-lg bg-red-50 p-4 text-sm text-red-700">
          Failed to load projects. Please try again.
        </p>
      )}

      {!isLoading && projects.length === 0 && (
        <p className="rounded-xl border border-dashed border-slate-300 p-10 text-center text-sm text-muted">
          No projects match your search.
        </p>
      )}

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        {projects.map((project) => (
          <ProjectCard key={project.id} project={project} />
        ))}
      </div>

      {hasNextPage && (
        <div className="mt-8 flex justify-center">
          <Button
            variant="outline"
            isLoading={isFetchingNextPage}
            onClick={() => fetchNextPage()}
          >
            Load more
          </Button>
        </div>
      )}
    </div>
  );
}
