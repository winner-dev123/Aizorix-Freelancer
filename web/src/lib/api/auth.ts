import { get, post } from '@/lib/api/client';
import type {
  AuthTokens,
  AuthUser,
  LoginRequest,
  MeResponse,
  RegisterRequest,
  UserRole,
} from '@/lib/types';

/** Map the raw `/v1/auth/me` payload onto the frontend `AuthUser` shape. */
function toAuthUser(me: MeResponse): AuthUser {
  // Prefer an elevated role (e.g. admin) from `roles`, else the account type.
  const role: UserRole = me.roles.includes('admin') ? 'admin' : me.account_type;
  return {
    id: me.user_id,
    email: me.email,
    role,
    // `/me` doesn't carry profile fields; derive a friendly default.
    display_name: me.email.split('@')[0] ?? me.email,
    avatar_url: null,
    verified: me.email_verified,
  };
}

/** Auth service — wraps /v1/auth/*. */
export const authApi = {
  register(input: RegisterRequest): Promise<AuthTokens> {
    return post<AuthTokens>('/v1/auth/register', input);
  },

  login(input: LoginRequest): Promise<AuthTokens> {
    return post<AuthTokens>('/v1/auth/login', input);
  },

  /** Exchange the httpOnly `aizorix_refresh` cookie for a new access token.
   *  Sends an empty body with credentials so the cookie is used. */
  refresh(): Promise<AuthTokens> {
    return post<AuthTokens>('/v1/auth/refresh', {}, { withCredentials: true });
  },

  /** Revoke the server-side session. The backend reads the httpOnly
   *  `aizorix_refresh` cookie, so we send an empty body with credentials.
   *  (The app never holds a refresh token in JS memory — it lives only in the
   *  cookie — so there's nothing to forward in the body.) */
  logout(): Promise<void> {
    return post<void>('/v1/auth/logout', {}, { withCredentials: true });
  },

  /** Resolve the currently authenticated user from the access token. */
  async me(): Promise<AuthUser> {
    const raw = await get<MeResponse>('/v1/auth/me');
    return toAuthUser(raw);
  },
};
