/**
 * In-memory access-token session.
 *
 * Security model:
 *  - The short-lived access token (JWT) lives ONLY in JS memory — never in
 *    localStorage — to limit XSS blast radius.
 *  - The long-lived refresh token is an httpOnly, Secure, SameSite cookie set
 *    by the gateway; JS cannot read it. The axios client calls the refresh
 *    endpoint (which sends that cookie automatically) to mint new access
 *    tokens when one expires.
 *  - On a hard page load the access token is gone, so the app performs a
 *    silent refresh on boot (see `useAuth`/`AuthProvider`).
 *
 * This module is the single source of truth for the current token and exposes
 * a tiny subscription so the axios interceptor and React stay in sync without
 * importing React here (it must be usable in plain module scope).
 */

import type { AuthUser } from '@/lib/types';

interface SessionState {
  accessToken: string | null;
  expiresAt: number | null; // epoch ms
  user: AuthUser | null;
}

const state: SessionState = {
  accessToken: null,
  expiresAt: null,
  user: null,
};

type Listener = (s: Readonly<SessionState>) => void;
const listeners = new Set<Listener>();

function emit(): void {
  for (const l of listeners) l(state);
}

export const session = {
  getAccessToken(): string | null {
    return state.accessToken;
  },

  getUser(): AuthUser | null {
    return state.user;
  },

  isAuthenticated(): boolean {
    return state.accessToken !== null && !this.isExpired();
  },

  isExpired(skewMs = 5_000): boolean {
    if (state.expiresAt === null) return true;
    return Date.now() >= state.expiresAt - skewMs;
  },

  /** Store a freshly issued access token. `expiresInSeconds` comes from the
   *  `access_expires_in` field of the AuthTokens response. */
  setAccessToken(token: string, expiresInSeconds: number): void {
    state.accessToken = token;
    state.expiresAt = Date.now() + expiresInSeconds * 1000;
    emit();
  },

  setUser(user: AuthUser | null): void {
    state.user = user;
    emit();
  },

  clear(): void {
    state.accessToken = null;
    state.expiresAt = null;
    state.user = null;
    emit();
  },

  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
};

export type { SessionState };
