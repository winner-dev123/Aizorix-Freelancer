/**
 * Axios instance with auth + silent-refresh interceptors.
 *
 * Request flow:
 *  1. Request interceptor attaches `Authorization: Bearer <accessToken>` from
 *     the in-memory session.
 *  2. On a 401, the response interceptor attempts a single token refresh using
 *     the httpOnly refresh cookie (sent automatically via `withCredentials`),
 *     then replays the original request. Concurrent 401s share one refresh
 *     promise so we never stampede the refresh endpoint.
 *  3. If refresh fails, the session is cleared and the failure propagates so
 *     route guards / UI can redirect to /login.
 */
import axios, {
  AxiosError,
  type AxiosInstance,
  type AxiosRequestConfig,
  type InternalAxiosRequestConfig,
} from 'axios';

import { session } from '@/lib/auth/session';
import type { ApiError, AuthTokens } from '@/lib/types';

const BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL ?? '/api/gateway';

/** Normalized error thrown by every service call. */
export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields?: Record<string, string>;

  constructor(status: number, body: Partial<ApiError> | undefined, fallback: string) {
    super(body?.message ?? fallback);
    this.name = 'ApiRequestError';
    this.status = status;
    this.code = body?.code ?? 'UNKNOWN';
    this.fields = body?.fields;
  }
}

export const api: AxiosInstance = axios.create({
  baseURL: BASE_URL,
  withCredentials: true, // send/receive the httpOnly refresh cookie
  headers: { 'Content-Type': 'application/json' },
  timeout: 20_000,
});

// ── Request: attach bearer token ──────────────────────────────────────────
api.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  const token = session.getAccessToken();
  if (token) {
    config.headers.set('Authorization', `Bearer ${token}`);
  }
  return config;
});

// ── Response: silent refresh on 401, then normalize errors ────────────────
type RetriableConfig = InternalAxiosRequestConfig & { _retried?: boolean };

let refreshPromise: Promise<boolean> | null = null;

/** Hit the refresh endpoint exactly once at a time. Returns success. */
async function refreshAccessToken(): Promise<boolean> {
  if (!refreshPromise) {
    refreshPromise = (async () => {
      try {
        // Bare axios (not `api`) to avoid recursive interceptor application.
        const { data } = await axios.post<AuthTokens>(
          `${BASE_URL}/v1/auth/refresh`,
          {},
          { withCredentials: true },
        );
        session.setAccessToken(data.access_token, data.access_expires_in);
        return true;
      } catch {
        session.clear();
        return false;
      } finally {
        // Reset on next tick so a fresh batch of 401s starts a new refresh.
        setTimeout(() => {
          refreshPromise = null;
        }, 0);
      }
    })();
  }
  return refreshPromise;
}

api.interceptors.response.use(
  (response) => response,
  async (error: AxiosError<ApiError>) => {
    const original = error.config as RetriableConfig | undefined;
    const status = error.response?.status ?? 0;

    const isAuthRoute = original?.url?.includes('/v1/auth/');
    if (status === 401 && original && !original._retried && !isAuthRoute) {
      original._retried = true;
      const refreshed = await refreshAccessToken();
      if (refreshed) {
        return api(original);
      }
    }

    throw new ApiRequestError(
      status,
      error.response?.data,
      error.message || 'Request failed',
    );
  },
);

// ── Thin typed helpers so service modules stay terse ──────────────────────
export async function get<T>(url: string, config?: AxiosRequestConfig): Promise<T> {
  const { data } = await api.get<T>(url, config);
  return data;
}

export async function post<T>(
  url: string,
  body?: unknown,
  config?: AxiosRequestConfig,
): Promise<T> {
  const { data } = await api.post<T>(url, body, config);
  return data;
}

export async function patch<T>(
  url: string,
  body?: unknown,
  config?: AxiosRequestConfig,
): Promise<T> {
  const { data } = await api.patch<T>(url, body, config);
  return data;
}

export async function del<T>(url: string, config?: AxiosRequestConfig): Promise<T> {
  const { data } = await api.delete<T>(url, config);
  return data;
}
