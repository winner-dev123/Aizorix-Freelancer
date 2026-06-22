import { get, post } from '@/lib/api/client';
import type {
  Paginated,
  Proposal,
  ProposalStatus,
  SubmitProposalInput,
  UUID,
} from '@/lib/types';

/** Proposal service — wraps the flat /v1/proposals surface. */
export const proposalsApi = {
  /** Submit a bid. May 402 if the freelancer lacks connects.
   *  `project_id` travels in the JSON body, not the path. */
  submit(input: SubmitProposalInput): Promise<Proposal> {
    return post<Proposal>('/v1/proposals', input);
  },

  /** Proposals the current freelancer has submitted. */
  listMine(cursor?: string): Promise<Paginated<Proposal>> {
    return get<Paginated<Proposal>>('/v1/proposals/mine', { params: { cursor } });
  },

  /** Proposals received for a project (client view). */
  listForProject(
    projectId: UUID,
    cursor?: string,
    status?: ProposalStatus,
  ): Promise<Paginated<Proposal>> {
    return get<Paginated<Proposal>>('/v1/proposals', {
      params: { project_id: projectId, status, cursor },
    });
  },
};
