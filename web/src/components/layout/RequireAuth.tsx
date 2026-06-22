'use client';

import { useRouter } from 'next/navigation';
import { useEffect, type ReactNode } from 'react';

import { Spinner } from '@/components/ui/Skeleton';
import { useAuth } from '@/hooks/useAuth';
import type { UserRole } from '@/lib/types';

export interface RequireAuthProps {
  children: ReactNode;
  /** If set, the user's role must be in this list. */
  roles?: UserRole[];
}

/**
 * Client-side guard that complements `middleware.ts`. Middleware blocks
 * unauthenticated navigation cheaply at the edge (cookie presence); this guard
 * resolves the actual user (silent refresh) and enforces role checks once the
 * access token is in memory.
 */
export function RequireAuth({ children, roles }: RequireAuthProps) {
  const router = useRouter();
  const { user, status, isLoading } = useAuth();

  useEffect(() => {
    if (status === 'unauthenticated') {
      router.replace('/login');
    } else if (roles && user && !roles.includes(user.role)) {
      router.replace('/marketplace');
    }
  }, [status, user, roles, router]);

  if (isLoading || status === 'unauthenticated') {
    return (
      <div className="flex h-[60vh] items-center justify-center">
        <Spinner className="h-8 w-8" />
      </div>
    );
  }

  if (roles && user && !roles.includes(user.role)) {
    return null;
  }

  return <>{children}</>;
}
