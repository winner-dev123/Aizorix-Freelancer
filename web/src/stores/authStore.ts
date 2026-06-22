import { create } from 'zustand';

import { session } from '@/lib/auth/session';
import type { AuthUser } from '@/lib/types';

/**
 * React-facing mirror of the in-memory `session`. The token itself stays in
 * `session` (so the axios interceptor can read it synchronously outside React),
 * while this store exposes the user + auth status reactively to components and
 * subscribes to session changes to stay in sync.
 *
 * Use `useAuth` for data fetching; use this store for cheap reads of the
 * current user and the boot/refresh status.
 */

export type AuthStatus = 'unknown' | 'authenticated' | 'unauthenticated';

interface AuthStoreState {
  user: AuthUser | null;
  status: AuthStatus;
  setUser: (user: AuthUser | null) => void;
  setStatus: (status: AuthStatus) => void;
  reset: () => void;
}

export const useAuthStore = create<AuthStoreState>((set) => ({
  user: session.getUser(),
  status: 'unknown',
  setUser: (user) => {
    session.setUser(user);
    set({ user, status: user ? 'authenticated' : 'unauthenticated' });
  },
  setStatus: (status) => set({ status }),
  reset: () => {
    session.clear();
    set({ user: null, status: 'unauthenticated' });
  },
}));

// Keep the store in lock-step with the underlying session (e.g. when the
// axios interceptor clears it after a failed refresh).
if (typeof window !== 'undefined') {
  session.subscribe((s) => {
    const state = useAuthStore.getState();
    if (s.user !== state.user) {
      useAuthStore.setState({
        user: s.user,
        status: s.user ? 'authenticated' : state.status,
      });
    }
    if (s.accessToken === null && state.status === 'authenticated') {
      useAuthStore.setState({ status: 'unauthenticated', user: null });
    }
  });
}
