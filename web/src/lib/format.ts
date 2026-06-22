/**
 * Display formatting helpers. The backend speaks integer cents and ISO
 * timestamps; these adapt those wire shapes for humans.
 */
import { formatDistanceToNow, format, intervalToDuration } from 'date-fns';

import type { Currency, ISODateString } from '@/lib/types';

const currencyLocale: Record<Currency, string> = {
  USD: 'en-US',
  EUR: 'de-DE',
  GBP: 'en-GB',
};

/** Format integer cents as a localized currency string. */
export function formatMoney(cents: number, currency: Currency = 'USD'): string {
  return new Intl.NumberFormat(currencyLocale[currency] ?? 'en-US', {
    style: 'currency',
    currency,
  }).format(cents / 100);
}

/** Compact money for cards/badges, e.g. "$1.2k". */
export function formatMoneyCompact(cents: number, currency: Currency = 'USD'): string {
  return new Intl.NumberFormat(currencyLocale[currency] ?? 'en-US', {
    style: 'currency',
    currency,
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(cents / 100);
}

/** Render a budget range like "$25.00 – $60.00 /hr". */
export function formatBudgetRange(
  minCents: number,
  maxCents: number,
  currency: Currency,
  hourly: boolean,
): string {
  const suffix = hourly ? ' /hr' : '';
  if (minCents === maxCents) return `${formatMoney(minCents, currency)}${suffix}`;
  return `${formatMoney(minCents, currency)} – ${formatMoney(maxCents, currency)}${suffix}`;
}

/** Absolute date, e.g. "Jun 22, 2026". */
export function formatDate(iso: ISODateString): string {
  return format(new Date(iso), 'MMM d, yyyy');
}

/** Date + time, e.g. "Jun 22, 2026, 3:40 PM". */
export function formatDateTime(iso: ISODateString): string {
  return format(new Date(iso), 'MMM d, yyyy, h:mm a');
}

/** Relative, e.g. "3 hours ago". */
export function formatRelative(iso: ISODateString): string {
  return formatDistanceToNow(new Date(iso), { addSuffix: true });
}

/** Time-only label for screenshot grid cells, e.g. "3:40 PM". */
export function formatTime(iso: ISODateString): string {
  return format(new Date(iso), 'h:mm a');
}

/** Seconds → "Hh Mm" tracked-time display. */
export function formatDuration(totalSeconds: number): string {
  const { hours = 0, minutes = 0 } = intervalToDuration({
    start: 0,
    end: totalSeconds * 1000,
  });
  if (hours === 0) return `${minutes}m`;
  return `${hours}h ${minutes}m`;
}

/** Clamp an activity percentage to a labelled severity tier. */
export function activityTier(pct: number): 'high' | 'medium' | 'low' {
  if (pct >= 60) return 'high';
  if (pct >= 30) return 'medium';
  return 'low';
}

export function formatPercent(pct: number): string {
  return `${Math.round(pct)}%`;
}

/** Human label for an ISO 3166-1 alpha-2 country code (best-effort). */
export function countryName(code: string): string {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' }).of(code.toUpperCase()) ?? code;
  } catch {
    return code;
  }
}
