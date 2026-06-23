import { get, post } from '@/lib/api/client';
import type { Proposal, ProposalStatus, SubmitProposalInput, UUID } from '@/lib/types';

/** Proposal service — wraps the flat /v1/proposals surface. Responses are bounded by
 *  limit/offset (default 50, cap 100) and returned under a `{ proposals: [...] }` envelope. */
export const proposalsApi = {
  /** Submit a bid. May 402 if the freelancer lacks connects.
   *  `project_id` travels in the JSON body, not the path. */
  submit(input: SubmitProposalInput): Promise<Proposal> {
    return post<Proposal>('/v1/proposals', input);
  },

  /** Proposals the current freelancer has submitted. */
  listMine(limit?: number, offset?: number): Promise<Proposal[]> {
    return get<{ proposals: Proposal[] }>('/v1/proposals/mine', {
      params: { limit, offset },
    }).then((r) => r.proposals);
  },

  /** Proposals received for a project (client view). */
  listForProject(
    projectId: UUID,
    status?: ProposalStatus,
  ): Promise<Proposal[]> {
    return get<{ proposals: Proposal[] }>('/v1/proposals', {
      params: { project_id: projectId, status },
    }).then((r) => r.proposals);
  },
};
