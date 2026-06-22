'use client';

import { useState } from 'react';
import Link from 'next/link';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { z } from 'zod';

import { PageHeader } from '@/components/layout/PageHeader';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/Card';
import { Input, Textarea } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { useContracts } from '@/hooks/useContract';
import { useCreateProject } from '@/hooks/useProjects';
import { formatMoney } from '@/lib/format';

const schema = z.object({
  title: z.string().min(8, 'Give your project a clear title'),
  description: z.string().min(40, 'Describe the work in at least 40 characters'),
  budget_type: z.enum(['fixed', 'hourly']),
  budget_min: z.coerce.number().positive(),
  budget_max: z.coerce.number().positive(),
  skills: z.string().min(1, 'Add at least one skill'),
});

type FormValues = z.infer<typeof schema>;

export default function ClientDashboardPage() {
  const [open, setOpen] = useState(false);
  const contracts = useContracts();
  const createProject = useCreateProject();

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { budget_type: 'hourly' },
  });

  const onSubmit = handleSubmit(async (v) => {
    await createProject.mutateAsync({
      title: v.title,
      description: v.description,
      budget_type: v.budget_type,
      budget_min_cents: Math.round(v.budget_min * 100),
      budget_max_cents: Math.round(v.budget_max * 100),
      currency: 'USD',
      skills: v.skills.split(',').map((s) => s.trim()).filter(Boolean),
    });
    reset();
    setOpen(false);
  });

  return (
    <div className="space-y-8">
      <PageHeader
        title="Client dashboard"
        description="Post work, review proposals, and manage contracts."
        actions={<Button onClick={() => setOpen(true)}>Post a project</Button>}
      />

      <Card>
        <CardHeader>
          <CardTitle>Your contracts</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {contracts.data?.items.map((c) => (
            <Link
              key={c.id}
              href={`/contracts/${c.id}`}
              className="flex items-center justify-between rounded-lg border border-slate-200 p-4 hover:bg-slate-50"
            >
              <div>
                <p className="font-medium text-slate-900">Contract {c.id.slice(0, 8)}</p>
                <p className="text-xs capitalize text-muted">
                  {c.type} · {c.milestones.length} milestones
                </p>
              </div>
              <div className="flex items-center gap-3">
                <Badge tone={c.status === 'active' ? 'success' : 'neutral'}>{c.status}</Badge>
                <span className="text-sm font-semibold text-slate-900">
                  {formatMoney(c.escrow_balance_cents, c.currency)}
                </span>
              </div>
            </Link>
          ))}
          {!contracts.isLoading && !contracts.data?.items.length && (
            <p className="text-sm text-muted">No contracts yet. Post a project to get started.</p>
          )}
        </CardContent>
      </Card>

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title="Post a new project"
        footer={
          <>
            <Button variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button onClick={onSubmit} isLoading={createProject.isPending}>
              Publish
            </Button>
          </>
        }
      >
        <form onSubmit={onSubmit} className="space-y-4">
          <Input label="Title" error={errors.title?.message} {...register('title')} />
          <Textarea
            label="Description"
            rows={5}
            error={errors.description?.message}
            {...register('description')}
          />
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="mb-1 block text-sm font-medium text-slate-700">Budget type</label>
              <select
                className="w-full rounded-lg border border-slate-300 px-3 py-2 text-sm focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500"
                {...register('budget_type')}
              >
                <option value="hourly">Hourly</option>
                <option value="fixed">Fixed price</option>
              </select>
            </div>
            <Input
              label="Skills (comma separated)"
              placeholder="React, TypeScript"
              error={errors.skills?.message}
              {...register('skills')}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Input
              type="number"
              step="0.01"
              label="Min budget (USD)"
              error={errors.budget_min?.message}
              {...register('budget_min')}
            />
            <Input
              type="number"
              step="0.01"
              label="Max budget (USD)"
              error={errors.budget_max?.message}
              {...register('budget_max')}
            />
          </div>
        </form>
      </Modal>
    </div>
  );
}
