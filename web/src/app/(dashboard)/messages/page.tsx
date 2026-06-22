'use client';

import { useState } from 'react';

import { PageHeader } from '@/components/layout/PageHeader';
import { MessageThread } from '@/components/features/MessageThread';
import { Avatar } from '@/components/ui/Avatar';
import { Badge } from '@/components/ui/Badge';
import { Card } from '@/components/ui/Card';
import { Spinner } from '@/components/ui/Skeleton';
import { useAuth } from '@/hooks/useAuth';
import { useThreads } from '@/hooks/useMessages';
import { formatRelative } from '@/lib/format';
import { cn } from '@/lib/utils';

export default function MessagesPage() {
  const { user } = useAuth();
  const { data: threads, isLoading } = useThreads();
  const [activeId, setActiveId] = useState<string | null>(null);

  const active = activeId ?? threads?.[0]?.id ?? null;

  return (
    <div className="flex h-[calc(100vh-8rem)] flex-col">
      <PageHeader title="Messages" />

      <Card className="grid flex-1 grid-cols-1 overflow-hidden md:grid-cols-[20rem_1fr]">
        {/* Thread list */}
        <aside className="border-r border-slate-200">
          {isLoading ? (
            <div className="flex justify-center p-8">
              <Spinner />
            </div>
          ) : threads && threads.length > 0 ? (
            <ul className="divide-y divide-slate-100 overflow-y-auto">
              {threads.map((t) => {
                const other = t.participant_names.find((n) => n !== user?.display_name);
                return (
                  <li key={t.id}>
                    <button
                      onClick={() => setActiveId(t.id)}
                      className={cn(
                        'flex w-full items-center gap-3 p-4 text-left transition hover:bg-slate-50',
                        active === t.id && 'bg-brand-50',
                      )}
                    >
                      <Avatar name={other ?? 'Conversation'} size={36} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between">
                          <p className="truncate font-medium text-slate-900">
                            {other ?? 'Conversation'}
                          </p>
                          {t.last_message && (
                            <span className="shrink-0 text-[10px] text-muted">
                              {formatRelative(t.last_message.sent_at)}
                            </span>
                          )}
                        </div>
                        <p className="truncate text-xs text-muted">
                          {t.last_message?.body ?? 'No messages yet'}
                        </p>
                      </div>
                      {t.unread_count > 0 && <Badge tone="brand">{t.unread_count}</Badge>}
                    </button>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="p-6 text-sm text-muted">No conversations yet.</p>
          )}
        </aside>

        {/* Active thread */}
        <section className="min-w-0">
          {active && user ? (
            <MessageThread threadId={active} currentUserId={user.id} />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-muted">
              Select a conversation to start chatting.
            </div>
          )}
        </section>
      </Card>
    </div>
  );
}
