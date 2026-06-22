'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { screenshotsApi } from '@/lib/api/screenshots';
import type { AuthorizedScreenshot, UUID } from '@/lib/types';

import { queryKeys } from './queryKeys';

/** Screenshot metadata for a contract (encrypted; no image bytes yet). */
export function useContractScreenshots(
  contractId: UUID | undefined,
  params: { from?: string; to?: string; session_id?: UUID } = {},
) {
  return useQuery({
    queryKey: queryKeys.screenshots.list(contractId ?? '', params.session_id),
    queryFn: () => screenshotsApi.listForContract(contractId as UUID, params),
    enabled: Boolean(contractId),
  });
}

/**
 * Authorize and (lazily) reveal a single screenshot. This is a mutation rather
 * than a query because each call is an audited decrypt-on-read event — we don't
 * want background refetches silently re-triggering the audit log. The result is
 * cached so the lightbox can read it back without re-authorizing.
 */
export function useAuthorizeScreenshot() {
  const qc = useQueryClient();
  return useMutation<AuthorizedScreenshot, Error, UUID>({
    mutationFn: (id) => screenshotsApi.authorize(id),
    onSuccess: (authorized) => {
      qc.setQueryData(queryKeys.screenshots.detail(authorized.id), authorized);
    },
  });
}
