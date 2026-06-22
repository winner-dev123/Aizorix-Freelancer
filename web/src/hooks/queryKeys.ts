/**
 * Centralized React Query key factory. Every hook derives its keys from here so
 * invalidation is consistent and refactors are type-safe.
 *
 * Convention: each domain exposes `all`, list/detail builders, and any nested
 * collections. Keys are tuples; the first element namespaces the domain.
 */
import type { ProjectSearchParams, UUID } from '@/lib/types';

export const queryKeys = {
  auth: {
    me: () => ['auth', 'me'] as const,
  },

  projects: {
    all: () => ['projects'] as const,
    list: (params: ProjectSearchParams) => ['projects', 'list', params] as const,
    detail: (id: UUID) => ['projects', 'detail', id] as const,
  },

  proposals: {
    all: () => ['proposals'] as const,
    mine: () => ['proposals', 'mine'] as const,
    forProject: (projectId: UUID) => ['proposals', 'project', projectId] as const,
  },

  contracts: {
    all: () => ['contracts'] as const,
    list: () => ['contracts', 'list'] as const,
    detail: (id: UUID) => ['contracts', 'detail', id] as const,
    timeline: (id: UUID) => ['contracts', 'detail', id, 'timeline'] as const,
  },

  timesheets: {
    all: (contractId: UUID) => ['timesheets', contractId] as const,
    week: (contractId: UUID, weekStart: string) =>
      ['timesheets', contractId, weekStart] as const,
  },

  screenshots: {
    all: (contractId: UUID) => ['screenshots', contractId] as const,
    list: (contractId: UUID, sessionId?: UUID) =>
      ['screenshots', contractId, { sessionId }] as const,
    detail: (id: UUID) => ['screenshots', 'detail', id] as const,
  },

  payments: {
    summary: () => ['payments', 'summary'] as const,
    transactions: () => ['payments', 'transactions'] as const,
  },

  messages: {
    threads: () => ['messages', 'threads'] as const,
    thread: (threadId: UUID) => ['messages', 'thread', threadId] as const,
  },
} as const;
