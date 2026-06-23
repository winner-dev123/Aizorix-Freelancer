import { get, post } from '@/lib/api/client';
import type { Contract, ContractEvent, Currency, UUID } from '@/lib/types';

/** Escrow account for a contract, from GET /v1/escrow?contract_id=... (escrow service). */
export interface EscrowAccount {
  escrow_id: UUID;
  contract_id: UUID;
  currency: Currency;
  held_cents: number;
  released_cents: number;
  refunded_cents: number;
  status: string;
}

/** Contract service — wraps /v1/contracts. List is role-scoped (the gateway-injected user
 *  must say which side they're on) and bounded by limit/offset; the detail response carries
 *  the contract's milestones; the timeline is the event-sourced transition log. */
export const contractsApi = {
  list(role: 'client' | 'freelancer', limit?: number, offset?: number): Promise<Contract[]> {
    return get<{ contracts: Contract[] }>('/v1/contracts', {
      params: { role, limit, offset },
    }).then((r) => r.contracts);
  },

  get(id: UUID): Promise<Contract> {
    return get<Contract>(`/v1/contracts/${id}`);
  },

  /** Activity timeline (event-sourced transitions) for the contract detail view. */
  timeline(id: UUID): Promise<ContractEvent[]> {
    return get<{ events: ContractEvent[] }>(`/v1/contracts/${id}/events`).then(
      (r) => r.events,
    );
  },

  /** Live escrow account for a contract (held / released / refunded). */
  escrow(contractId: UUID): Promise<EscrowAccount> {
    return get<EscrowAccount>('/v1/escrow', { params: { contract_id: contractId } });
  },

  /** Approve a milestone and release escrow (client only). Milestones are addressed by
   *  their global id. Returns `{ success }`; callers refetch the contract afterward. */
  approveMilestone(milestoneId: UUID): Promise<{ success: boolean }> {
    return post<{ success: boolean }>(`/v1/contracts/milestones/${milestoneId}/approve`, {});
  },
};
