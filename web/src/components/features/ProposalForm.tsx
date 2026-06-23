'use client';

import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { z } from 'zod';

import { Button } from '@/components/ui/Button';
import { Input, Textarea } from '@/components/ui/Input';
import { ApiRequestError } from '@/lib/api/client';
import type { BudgetType, SubmitProposalInput, UUID } from '@/lib/types';

/** Validation schema. Rate is captured in major units and converted to cents. */
const schema = z.object({
  cover_letter: z
    .string()
    .min(50, 'Cover letter must be at least 50 characters')
    .max(5000),
  bid_rate: z.coerce.number().positive('Enter a positive amount'),
  estimated_days: z.coerce.number().int().positive().optional(),
});

type FormValues = z.infer<typeof schema>;

export interface ProposalFormProps {
  projectId: UUID;
  budgetType: BudgetType;
  /** Connects required to submit; shown to the user. */
  connectsCost?: number;
  isSubmitting?: boolean;
  error?: unknown;
  onSubmit: (input: SubmitProposalInput) => void;
}

export function ProposalForm({
  projectId,
  budgetType,
  connectsCost = 2,
  isSubmitting,
  error,
  onSubmit,
}: ProposalFormProps) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) });

  const submit = handleSubmit((values) => {
    onSubmit({
      project_id: projectId,
      cover_letter: values.cover_letter,
      bid_amount_cents: Math.round(values.bid_rate * 100),
      currency: 'USD',
      estimated_duration_days: values.estimated_days,
    });
  });

  const apiError = error instanceof ApiRequestError ? error : null;
  const insufficientConnects = apiError?.status === 402;

  return (
    <form onSubmit={submit} className="space-y-5">
      <Textarea
        label="Cover letter"
        placeholder="Explain why you're a great fit for this project…"
        rows={8}
        error={errors.cover_letter?.message}
        {...register('cover_letter')}
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Input
          type="number"
          step="0.01"
          min="0"
          label={budgetType === 'hourly' ? 'Hourly rate (USD)' : 'Fixed bid (USD)'}
          placeholder={budgetType === 'hourly' ? '45.00' : '2500.00'}
          error={errors.bid_rate?.message}
          {...register('bid_rate')}
        />
        {budgetType === 'hourly' && (
          <Input
            type="number"
            min="1"
            label="Estimated duration (days)"
            placeholder="30"
            error={errors.estimated_days?.message}
            {...register('estimated_days')}
          />
        )}
      </div>

      {insufficientConnects && (
        <p className="rounded-lg bg-amber-50 p-3 text-sm text-amber-800">
          You don&apos;t have enough connects to submit. Top up to continue.
        </p>
      )}
      {apiError && !insufficientConnects && (
        <p className="rounded-lg bg-red-50 p-3 text-sm text-red-700">{apiError.message}</p>
      )}

      <div className="flex items-center justify-between border-t border-slate-100 pt-4">
        <span className="text-sm text-muted">
          Submitting costs <strong>{connectsCost} connects</strong>
        </span>
        <Button type="submit" isLoading={isSubmitting}>
          Submit proposal
        </Button>
      </div>
    </form>
  );
}
