'use client';

import { useState } from 'react';

import { ActivityBar } from '@/components/features/ActivityBar';
import { Badge, type BadgeTone } from '@/components/ui/Badge';
import { Modal } from '@/components/ui/Modal';
import { Spinner } from '@/components/ui/Skeleton';
import { useAuthorizeScreenshot } from '@/hooks/useScreenshots';
import { formatPercent, formatTime } from '@/lib/format';
import { cn } from '@/lib/utils';
import type { Screenshot, ScreenshotFlag } from '@/lib/types';

const flagMeta: Record<ScreenshotFlag, { tone: BadgeTone; label: string } | null> = {
  none: null,
  low_activity: { tone: 'warning', label: 'Low activity' },
  duplicate: { tone: 'warning', label: 'Duplicate' },
  manual_review: { tone: 'info', label: 'Review' },
  blocked: { tone: 'danger', label: 'Blocked' },
};

export interface ScreenshotGridProps {
  screenshots: Screenshot[];
  isLoading?: boolean;
}

/**
 * Review grid of work screenshots. Each tile shows activity % and any fraud
 * flag. Tiles are encrypted by default; clicking authorizes an audited
 * decrypt-on-read and opens the decrypted image in a lightbox.
 */
export function ScreenshotGrid({ screenshots, isLoading }: ScreenshotGridProps) {
  const authorize = useAuthorizeScreenshot();
  const [activeId, setActiveId] = useState<string | null>(null);

  const reveal = (s: Screenshot) => {
    setActiveId(s.id);
    if (s.encrypted) authorize.mutate(s.id);
  };

  const active = screenshots.find((s) => s.id === activeId) ?? null;
  const authorized =
    authorize.data && authorize.data.id === activeId ? authorize.data : null;

  if (isLoading) {
    return (
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
        {Array.from({ length: 8 }).map((_, i) => (
          <div key={i} className="aspect-video animate-pulse rounded-xl bg-slate-200" />
        ))}
      </div>
    );
  }

  if (screenshots.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-slate-300 p-10 text-center text-sm text-muted">
        No screenshots captured for this period.
      </div>
    );
  }

  return (
    <>
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
        {screenshots.map((s) => {
          const flag = flagMeta[s.flag];
          return (
            <button
              key={s.id}
              onClick={() => reveal(s)}
              className={cn(
                'group relative flex aspect-video flex-col justify-between overflow-hidden rounded-xl border bg-slate-100 p-2 text-left transition',
                'hover:ring-2 hover:ring-brand-400',
                s.flag === 'blocked' ? 'border-danger' : 'border-slate-200',
              )}
            >
              {/* Encrypted placeholder — real thumbnail only after authorize */}
              <div className="absolute inset-0 flex items-center justify-center text-slate-400">
                <span className="text-2xl" aria-hidden>
                  🔒
                </span>
              </div>
              <div className="relative flex justify-between">
                <Badge tone="neutral">{formatTime(s.captured_at)}</Badge>
                {flag && <Badge tone={flag.tone}>{flag.label}</Badge>}
              </div>
              <div className="relative rounded-md bg-white/85 px-2 py-1 backdrop-blur">
                <ActivityBar pct={s.activity_pct} />
              </div>
            </button>
          );
        })}
      </div>

      <Modal
        open={active !== null}
        onClose={() => setActiveId(null)}
        title={active ? `Screenshot · ${formatTime(active.captured_at)}` : undefined}
        className="max-w-3xl"
      >
        {active && (
          <div className="space-y-4">
            <div className="flex aspect-video items-center justify-center overflow-hidden rounded-lg bg-slate-100">
              {authorize.isPending && <Spinner />}
              {authorize.isError && (
                <p className="px-6 text-center text-sm text-danger">
                  Not authorized to view this screenshot, or it has expired.
                </p>
              )}
              {authorized && (
                // In production the signed_url points to the encrypted object and
                // the image is decrypted client-side using decryption_key + nonce
                // before being rendered. Shown directly here for the scaffold.
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  src={authorized.signed_url}
                  alt={`Work screenshot captured at ${formatTime(active.captured_at)}`}
                  className="h-full w-full object-contain"
                />
              )}
            </div>
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted">
                Activity{' '}
                <strong className="text-slate-900">
                  {formatPercent(active.activity_pct)}
                </strong>
              </span>
              {flagMeta[active.flag] && (
                <Badge tone={flagMeta[active.flag]!.tone}>{flagMeta[active.flag]!.label}</Badge>
              )}
            </div>
            {active.memo && <p className="text-sm text-slate-700">{active.memo}</p>}
            <p className="text-xs text-muted">
              Viewing this screenshot is logged to the contract audit trail.
            </p>
          </div>
        )}
      </Modal>
    </>
  );
}
