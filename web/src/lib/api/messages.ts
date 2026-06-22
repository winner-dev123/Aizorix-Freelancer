import { get, post } from '@/lib/api/client';
import type { Message, MessageThread, UUID } from '@/lib/types';

/** Messaging service — wraps /v1/messaging. Realtime delivery is over WS
 *  (NEXT_PUBLIC_WS_URL); these REST calls cover history + send fallback. */
export const messagesApi = {
  threads(): Promise<MessageThread[]> {
    return get<MessageThread[]>('/v1/messaging/threads');
  },

  messages(threadId: UUID, cursor?: string): Promise<Message[]> {
    return get<Message[]>(`/v1/messaging/threads/${threadId}/messages`, {
      params: { cursor },
    });
  },

  send(threadId: UUID, body: string): Promise<Message> {
    return post<Message>(`/v1/messaging/threads/${threadId}/messages`, { body });
  },

  markRead(threadId: UUID): Promise<void> {
    return post<void>(`/v1/messaging/threads/${threadId}/read`, {});
  },
};
