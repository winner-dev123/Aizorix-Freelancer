import { get, post } from '@/lib/api/client';
import type { Message, MessageThread, UUID } from '@/lib/types';

/** Messaging service — wraps /v1/messaging/conversations. Realtime delivery is over WS
 *  (NEXT_PUBLIC_WS_URL); these REST calls cover history + a send fallback. Responses use
 *  `{ conversations: [...] }` / `{ messages: [...] }` envelopes. */
export const messagesApi = {
  threads(): Promise<MessageThread[]> {
    return get<{ conversations: MessageThread[] }>('/v1/messaging/conversations').then(
      (r) => r.conversations,
    );
  },

  /** Message history for a conversation, newest-first. `before` is an RFC3339 timestamp
   *  for keyset paging (the backend's cursor for messages). */
  messages(conversationId: UUID, before?: string): Promise<Message[]> {
    return get<{ messages: Message[] }>(
      `/v1/messaging/conversations/${conversationId}/messages`,
      { params: { before } },
    ).then((r) => r.messages);
  },

  send(conversationId: UUID, body: string): Promise<Message> {
    return post<Message>(`/v1/messaging/conversations/${conversationId}/messages`, {
      body,
      kind: 'text',
    });
  },

  markRead(conversationId: UUID): Promise<void> {
    return post<void>(`/v1/messaging/conversations/${conversationId}/read`, {});
  },
};
