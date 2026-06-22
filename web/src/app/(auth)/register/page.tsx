'use client';

import { zodResolver } from '@hookform/resolvers/zod';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useForm } from 'react-hook-form';
import { z } from 'zod';

import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useAuth } from '@/hooks/useAuth';
import { ApiRequestError } from '@/lib/api/client';
import { cn } from '@/lib/utils';

const schema = z
  .object({
    email: z.string().email('Enter a valid email'),
    password: z
      .string()
      .min(12, 'Password must be at least 12 characters'),
    account_type: z.enum(['client', 'freelancer']),
    residency_country: z
      .string()
      .length(2, 'Use a 2-letter country code')
      .transform((s) => s.toUpperCase()),
    accepted_terms: z.literal(true, {
      errorMap: () => ({ message: 'You must accept the terms' }),
    }),
    accepted_monitoring_disclosure: z.boolean().optional(),
  })
  .refine(
    (v) => v.account_type !== 'freelancer' || v.accepted_monitoring_disclosure === true,
    {
      path: ['accepted_monitoring_disclosure'],
      message: 'Freelancers must accept the activity-monitoring disclosure',
    },
  );

type FormValues = z.input<typeof schema>;

export default function RegisterPage() {
  const router = useRouter();
  const { register: registerUser, registerState } = useAuth();
  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { account_type: 'freelancer' },
  });

  const accountType = watch('account_type');

  const onSubmit = handleSubmit(async (values) => {
    try {
      const me = await registerUser({
        email: values.email,
        password: values.password,
        account_type: values.account_type,
        residency_country: values.residency_country,
        accepted_terms: values.accepted_terms,
        accepted_monitoring_disclosure: values.accepted_monitoring_disclosure,
      });
      router.replace(me.role === 'client' ? '/client' : '/freelancer');
    } catch {
      // surfaced below
    }
  });

  const err = registerState.error;
  const message =
    err instanceof ApiRequestError
      ? err.status === 409
        ? 'That email is already registered.'
        : err.message
      : null;

  return (
    <div>
      <h1 className="text-2xl font-bold text-slate-900">Create your account</h1>
      <p className="mt-1 text-sm text-muted">Join Aizorix in under a minute.</p>

      <form onSubmit={onSubmit} className="mt-8 space-y-4">
        <div>
          <span className="mb-1 block text-sm font-medium text-slate-700">I want to</span>
          <div className="grid grid-cols-2 gap-3">
            {(['freelancer', 'client'] as const).map((type) => (
              <label
                key={type}
                className={cn(
                  'cursor-pointer rounded-lg border p-3 text-center text-sm font-medium capitalize transition',
                  accountType === type
                    ? 'border-brand-500 bg-brand-50 text-brand-700'
                    : 'border-slate-300 text-slate-600 hover:bg-slate-50',
                )}
              >
                <input type="radio" value={type} className="sr-only" {...register('account_type')} />
                {type === 'freelancer' ? 'Work as a freelancer' : 'Hire freelancers'}
              </label>
            ))}
          </div>
        </div>

        <Input
          type="email"
          label="Email"
          autoComplete="email"
          error={errors.email?.message}
          {...register('email')}
        />
        <Input
          type="password"
          label="Password"
          autoComplete="new-password"
          hint="At least 12 characters."
          error={errors.password?.message}
          {...register('password')}
        />
        <Input
          label="Country of residence"
          placeholder="US"
          maxLength={2}
          error={errors.residency_country?.message}
          {...register('residency_country')}
        />

        <label className="flex items-start gap-2 text-sm text-slate-700">
          <input type="checkbox" className="mt-0.5" {...register('accepted_terms')} />
          <span>I agree to the Terms of Service and Privacy Policy.</span>
        </label>
        {errors.accepted_terms && (
          <p className="text-xs text-danger">{errors.accepted_terms.message}</p>
        )}

        {accountType === 'freelancer' && (
          <>
            <label className="flex items-start gap-2 text-sm text-slate-700">
              <input
                type="checkbox"
                className="mt-0.5"
                {...register('accepted_monitoring_disclosure')}
              />
              <span>
                I understand that hourly contracts capture encrypted screenshots
                and activity metrics via the desktop tracker.
              </span>
            </label>
            {errors.accepted_monitoring_disclosure && (
              <p className="text-xs text-danger">
                {errors.accepted_monitoring_disclosure.message}
              </p>
            )}
          </>
        )}

        {message && (
          <p className="rounded-lg bg-red-50 p-3 text-sm text-red-700">{message}</p>
        )}

        <Button type="submit" className="w-full" isLoading={registerState.isPending}>
          Create account
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-muted">
        Already have an account?{' '}
        <Link href="/login" className="font-medium text-brand-600 hover:underline">
          Sign in
        </Link>
      </p>
    </div>
  );
}
