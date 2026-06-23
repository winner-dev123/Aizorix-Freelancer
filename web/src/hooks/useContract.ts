'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { contractsApi } from '@/lib/api/contracts';
import { paymentsApi } from '@/lib/api/payments';
import type { UUID } from '@/lib/types';

import { queryKeys } from './queryKeys';

/** List the current user's contracts for the given side (client/freelancer). */
export function useContracts(role: 'client' | 'freelancer') {
  return useQuery({
    queryKey: queryKeys.contracts.list(role),
    queryFn: () => contractsApi.list(role),
  });
}

/** Single contract detail (includes milestones). */
export function useContract(id: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.contracts.detail(id ?? ''),
    queryFn: () => contractsApi.get(id as UUID),
    enabled: Boolean(id),
  });
}

/** Live escrow account (held/released) for the contract detail sidebar. */
export function useContractEscrow(id: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.contracts.escrow(id ?? ''),
    queryFn: () => contractsApi.escrow(id as UUID),
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
    // approve returns { success }, not the contract — refetch the detail/escrow/timeline.
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.contracts.detail(contractId) });
      void qc.invalidateQueries({ queryKey: queryKeys.contracts.escrow(contractId) });
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
