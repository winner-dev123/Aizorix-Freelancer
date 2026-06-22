import { create } from 'zustand';

/**
 * Light, ephemeral UI state that doesn't belong in the server cache:
 * sidebar/drawer visibility, the active screenshot in the review lightbox,
 * and transient toasts. Persisted theme could be added with zustand/persist.
 */

export interface Toast {
  id: string;
  title: string;
  description?: string;
  variant: 'info' | 'success' | 'warning' | 'error';
}

interface UiState {
  sidebarOpen: boolean;
  /** Screenshot currently open in the review lightbox, or null. */
  lightboxScreenshotId: string | null;
  toasts: Toast[];

  toggleSidebar: () => void;
  setSidebar: (open: boolean) => void;
  openLightbox: (screenshotId: string) => void;
  closeLightbox: () => void;
  pushToast: (toast: Omit<Toast, 'id'>) => void;
  dismissToast: (id: string) => void;
}

export const useUiStore = create<UiState>((set) => ({
  sidebarOpen: true,
  lightboxScreenshotId: null,
  toasts: [],

  toggleSidebar: () => set((s) => ({ sidebarOpen: !s.sidebarOpen })),
  setSidebar: (open) => set({ sidebarOpen: open }),
  openLightbox: (screenshotId) => set({ lightboxScreenshotId: screenshotId }),
  closeLightbox: () => set({ lightboxScreenshotId: null }),

  pushToast: (toast) =>
    set((s) => ({
      toasts: [...s.toasts, { ...toast, id: crypto.randomUUID() }],
    })),
  dismissToast: (id) =>
    set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));
