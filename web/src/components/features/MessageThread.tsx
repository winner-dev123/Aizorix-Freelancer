'use client';

import { useEffect, useRef, useState, type FormEvent } from 'react';

import { Avatar } from '@/components/ui/Avatar';
import { Button } from '@/components/ui/Button';
import { Spinner } from '@/components/ui/Skeleton';
import { useSendMessage, useThreadMessages } from '@/hooks/useMessages';
import { formatTime } from '@/lib/format';
import { cn } from '@/lib/utils';
import type { UUID } from '@/lib/types';

export interface MessageThreadProps {
  threadId: UUID;
  /** Current user id, to align own messages to the right. */
  currentUserId: UUID;
}

export function MessageThread({ threadId, currentUserId }: MessageThreadProps) {
  const { data: messages, isLoading } = useThreadMessages(threadId);
  const send = useSendMessage(threadId);
  const [draft, setDraft] = useState('');
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages?.length]);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const body = draft.trim();
    if (!body) return;
    send.mutate(body);
    setDraft('');
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 space-y-3 overflow-y-auto p-4">
        {isLoading && (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        )}
        {messages?.map((m) => {
          const mine = m.sender_id === currentUserId || m.sender_id === 'me';
          return (
            <div key={m.id} className={cn('flex gap-2', mine && 'flex-row-reverse')}>
              {!mine && <Avatar name={m.sender_name} size={28} />}
              <div
                className={cn(
                  'max-w-[75%] rounded-2xl px-3 py-2 text-sm',
                  mine
                    ? 'bg-brand-600 text-white'
                    : 'bg-slate-100 text-slate-800',
                )}
              >
                <p className="whitespace-pre-wrap break-words">{m.body}</p>
                <span
                  className={cn(
                    'mt-1 block text-[10px]',
                    mine ? 'text-brand-100' : 'text-muted',
                  )}
                >
                  {formatTime(m.sent_at)}
                </span>
              </div>
            </div>
          );
        })}
        <div ref={bottomRef} />
      </div>

      <form
        onSubmit={onSubmit}
        className="flex items-center gap-2 border-t border-slate-100 p-3"
      >
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Write a message…"
          className="flex-1 rounded-full border border-slate-300 px-4 py-2 text-sm focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500"
        />
        <Button type="submit" size="sm" disabled={!draft.trim() || send.isPending}>
          Send
        </Button>
      </form>
    </div>
  );
}
