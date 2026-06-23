'use client';

import { zodResolver } from '@hookform/resolvers/zod';
import type { Route } from 'next';
import Link from 'next/link';
import { useRouter, useSearchParams } from 'next/navigation';
import { Suspense } from 'react';
import { useForm } from 'react-hook-form';
import { z } from 'zod';

import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useAuth } from '@/hooks/useAuth';
import { ApiRequestError } from '@/lib/api/client';

const schema = z.object({
  email: z.string().email('Enter a valid email'),
  password: z.string().min(1, 'Password is required'),
});

type FormValues = z.infer<typeof schema>;

function LoginForm() {
  const router = useRouter();
  const params = useSearchParams();
  const { login, loginState } = useAuth();
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema) });

  const onSubmit = handleSubmit(async (values) => {
    try {
      const me = await login(values);
      // The ?next= target is an arbitrary runtime string. Only accept it when it is a
      // same-origin relative path (single leading '/', not '//' which is protocol-relative
      // and would redirect off-origin) to prevent open-redirect/phishing. The literal
      // fallbacks are already typed routes; cast the validated path for the typedRoutes checker.
      const raw = params.get('next');
      const next = raw && raw.startsWith('/') && !raw.startsWith('//') ? (raw as Route) : null;
      const fallback: Route = me.role === 'admin' ? '/admin' : '/marketplace';
      router.replace(next ?? fallback);
    } catch {
      // surfaced below via loginState.error
    }
  });

  const err = loginState.error;
  const message =
    err instanceof ApiRequestError
      ? err.status === 423
        ? 'This account is temporarily locked. Try again later.'
        : err.status === 401
          ? 'Invalid email or password.'
          : err.message
      : null;

  return (
    <div>
      <h1 className="text-2xl font-bold text-slate-900">Welcome back</h1>
      <p className="mt-1 text-sm text-muted">Sign in to your Aizorix account.</p>

      <form onSubmit={onSubmit} className="mt-8 space-y-4">
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
          autoComplete="current-password"
          error={errors.password?.message}
          {...register('password')}
        />
        {message && (
          <p className="rounded-lg bg-red-50 p-3 text-sm text-red-700">{message}</p>
        )}
        <Button type="submit" className="w-full" isLoading={loginState.isPending}>
          Sign in
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-muted">
        New to Aizorix?{' '}
        <Link href="/register" className="font-medium text-brand-600 hover:underline">
          Create an account
        </Link>
      </p>
    </div>
  );
}

// useSearchParams() requires a Suspense boundary so the page can statically render its shell
// and hydrate the query-param-dependent form on the client (Next App Router CSR bailout).
export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginForm />
    </Suspense>
  );
}
