'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useRouter } from 'next/navigation';
import { useCallback, useEffect } from 'react';

import { authApi } from '@/lib/api/auth';
import { session } from '@/lib/auth/session';
import type { AuthTokens, LoginRequest, RegisterRequest } from '@/lib/types';
import { useAuthStore } from '@/stores/authStore';

import { queryKeys } from './queryKeys';

/** Persist a token bundle into the in-memory session. */
function adoptTokens(tokens: AuthTokens): void {
  session.setAccessToken(tokens.access_token, tokens.access_expires_in);
}

/**
 * Primary auth hook. Exposes the current user/status plus login, register and
 * logout mutations. On mount it performs a silent refresh so a page reload
 * (which loses the in-memory access token) re-establishes the session from the
 * httpOnly refresh cookie.
 */
export function useAuth() {
  const router = useRouter();
  const qc = useQueryClient();
  const { user, status, setUser, setStatus, reset } = useAuthStore();

  // Silent boot refresh: only run when we don't yet know who the user is.
  useEffect(() => {
    if (status !== 'unknown') return;
    let cancelled = false;
    (async () => {
      try {
        if (session.isExpired()) {
          const tokens = await authApi.refresh();
          adoptTokens(tokens);
        }
        const me = await authApi.me();
        if (!cancelled) setUser(me);
      } catch {
        if (!cancelled) setStatus('unauthenticated');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [status, setUser, setStatus]);

  const meQuery = useQuery({
    queryKey: queryKeys.auth.me(),
    queryFn: authApi.me,
    enabled: status === 'authenticated',
    initialData: user ?? undefined,
  });

  const loginMutation = useMutation({
    mutationFn: async (input: LoginRequest) => {
      const tokens = await authApi.login(input);
      adoptTokens(tokens);
      return authApi.me();
    },
    onSuccess: (me) => {
      setUser(me);
      qc.setQueryData(queryKeys.auth.me(), me);
    },
  });

  const registerMutation = useMutation({
    mutationFn: async (input: RegisterRequest) => {
      const tokens = await authApi.register(input);
      adoptTokens(tokens);
      return authApi.me();
    },
    onSuccess: (me) => {
      setUser(me);
      qc.setQueryData(queryKeys.auth.me(), me);
    },
  });

  const logout = useCallback(async () => {
    try {
      await authApi.logout();
    } finally {
      reset();
      qc.clear();
      router.push('/login');
    }
  }, [reset, qc, router]);

  return {
    user: meQuery.data ?? user,
    status,
    isAuthenticated: status === 'authenticated',
    isLoading: status === 'unknown',
    login: loginMutation.mutateAsync,
    loginState: loginMutation,
    register: registerMutation.mutateAsync,
    registerState: registerMutation,
    logout,
  };
}
