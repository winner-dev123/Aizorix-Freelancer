'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { contractsApi } from '@/lib/api/contracts';
import { paymentsApi } from '@/lib/api/payments';
import type { UUID } from '@/lib/types';

import { queryKeys } from './queryKeys';

/** List the current user's contracts. */
export function useContracts() {
  return useQuery({
    queryKey: queryKeys.contracts.list(),
    queryFn: () => contractsApi.list(),
  });
}

/** Single contract detail. */
export function useContract(id: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.contracts.detail(id ?? ''),
    queryFn: () => contractsApi.get(id as UUID),
    enabled: Boolean(id),
  });
}

/** Contract activity timeline. */
export function useContractTimeline(id: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.contracts.timeline(id ?? ''),
    queryFn: () => contractsApi.timeline(id as UUID),
    enabled: Boolean(id),
  });
}

/** Approve a milestone and release escrow (client). */
export function useApproveMilestone(contractId: UUID) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (milestoneId: UUID) => contractsApi.approveMilestone(milestoneId),
    onSuccess: (updated) => {
      qc.setQueryData(queryKeys.contracts.detail(contractId), updated);
      void qc.invalidateQueries({ queryKey: queryKeys.contracts.timeline(contractId) });
      void qc.invalidateQueries({ queryKey: queryKeys.payments.summary() });
    },
  });
}

/** Fund a milestone's escrow (client). */
export function useFundMilestone(contractId: UUID) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (seq: number) => paymentsApi.fundMilestone(contractId, seq),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.contracts.detail(contractId) });
      void qc.invalidateQueries({ queryKey: queryKeys.payments.summary() });
    },
  });
}
