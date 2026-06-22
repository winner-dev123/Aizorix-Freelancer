'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { messagesApi } from '@/lib/api/messages';
import type { Message, UUID } from '@/lib/types';

import { queryKeys } from './queryKeys';

/** All conversation threads for the current user. */
export function useThreads() {
  return useQuery({
    queryKey: queryKeys.messages.threads(),
    queryFn: () => messagesApi.threads(),
    // Threads carry unread counts; poll as a fallback to the WS channel.
    refetchInterval: 30_000,
  });
}

/** Messages within a thread. */
export function useThreadMessages(threadId: UUID | undefined) {
  return useQuery({
    queryKey: queryKeys.messages.thread(threadId ?? ''),
    queryFn: () => messagesApi.messages(threadId as UUID),
    enabled: Boolean(threadId),
  });
}

/** Send a message with optimistic append. */
export function useSendMessage(threadId: UUID) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: string) => messagesApi.send(threadId, body),
    onMutate: async (body) => {
      const key = queryKeys.messages.thread(threadId);
      await qc.cancelQueries({ queryKey: key });
      const previous = qc.getQueryData<Message[]>(key);
      const optimistic: Message = {
        id: `optimistic-${crypto.randomUUID()}`,
        thread_id: threadId,
        sender_id: 'me',
        sender_name: 'You',
        body,
        sent_at: new Date().toISOString(),
        read_at: null,
      };
      qc.setQueryData<Message[]>(key, [...(previous ?? []), optimistic]);
      return { previous };
    },
    onError: (_err, _body, ctx) => {
      if (ctx?.previous) {
        qc.setQueryData(queryKeys.messages.thread(threadId), ctx.previous);
      }
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.messages.thread(threadId) });
      void qc.invalidateQueries({ queryKey: queryKeys.messages.threads() });
    },
  });
}

/** Mark a thread read. */
export function useMarkThreadRead() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (threadId: UUID) => messagesApi.markRead(threadId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.messages.threads() });
    },
  });
}
