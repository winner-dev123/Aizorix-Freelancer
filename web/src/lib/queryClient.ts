import { QueryClient } from '@tanstack/react-query';

import { ApiRequestError } from '@/lib/api/client';

/**
 * Factory for a per-request QueryClient. A factory (not a module singleton) is
 * required for the App Router so server and client never share cache state.
 */
export function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        gcTime: 5 * 60_000,
        retry: (failureCount, error) => {
          // Never retry auth/permission/validation errors — only transient ones.
          if (error instanceof ApiRequestError) {
            if ([400, 401, 403, 404, 409, 422].includes(error.status)) return false;
          }
          return failureCount < 2;
        },
        refetchOnWindowFocus: false,
      },
      mutations: {
        retry: 0,
      },
    },
  });
}
