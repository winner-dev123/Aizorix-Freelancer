import { get, post } from '@/lib/api/client';
import type { Contract, ContractEvent, Paginated, UUID } from '@/lib/types';

/** Contract service — wraps /v1/contracts. */
export const contractsApi = {
  list(cursor?: string): Promise<Paginated<Contract>> {
    return get<Paginated<Contract>>('/v1/contracts', { params: { cursor } });
  },

  get(id: UUID): Promise<Contract> {
    return get<Contract>(`/v1/contracts/${id}`);
  },

  /** Activity timeline for the contract detail view. */
  timeline(id: UUID): Promise<ContractEvent[]> {
    return get<ContractEvent[]>(`/v1/contracts/${id}/events`);
  },

  /** Approve a milestone and release escrow (client only). Milestones are
   *  addressed by their global id, not contract-id + sequence. */
  approveMilestone(milestoneId: UUID): Promise<Contract> {
    return post<Contract>(`/v1/contracts/milestones/${milestoneId}/approve`, {});
  },
};
