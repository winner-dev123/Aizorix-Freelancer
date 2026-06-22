'use client';

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';

import { projectsApi } from '@/lib/api/projects';
import { proposalsApi } from '@/lib/api/proposals';
import type {
  CreateProjectInput,
  ProjectSearchParams,
  SubmitProposalInput,
  UUID,
} from '@/lib/types';

import { queryKeys } from './queryKeys';

/** Cursor-paginated marketplace search. */
export function useProjectSearch(params: ProjectSearchParams = {}) {
  return useInfiniteQuery({
    queryKey: queryKeys.projects.list(params),
    queryFn: ({ pageParam }) => projectsApi.search({ ...params, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

/** Single project detail. */
export function useProject(id: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.projects.detail(id ?? ''),
    queryFn: () => projectsApi.get(id as UUID),
    enabled: Boolean(id),
  });
}

/** Create a project (client). */
export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateProjectInput) => projectsApi.create(input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.projects.all() });
    },
  });
}

/** Submit a proposal to a project (freelancer). */
export function useSubmitProposal() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: SubmitProposalInput) => proposalsApi.submit(input),
    onSuccess: (proposal) => {
      void qc.invalidateQueries({ queryKey: queryKeys.proposals.mine() });
      void qc.invalidateQueries({
        queryKey: queryKeys.projects.detail(proposal.project_id),
      });
    },
  });
}

/** Proposals the current freelancer has submitted. */
export function useMyProposals() {
  return useQuery({
    queryKey: queryKeys.proposals.mine(),
    queryFn: () => proposalsApi.listMine(),
  });
}
