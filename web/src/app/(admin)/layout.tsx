import Link from 'next/link';
import type { ReactNode } from 'react';

import { RequireAuth } from '@/components/layout/RequireAuth';
import { Topbar } from '@/components/layout/Topbar';

export default function AdminLayout({ children }: { children: ReactNode }) {
  return (
    <RequireAuth roles={['admin']}>
      <div className="flex min-h-screen bg-slate-50">
        <aside className="hidden w-60 shrink-0 border-r border-slate-200 bg-slate-900 text-slate-100 md:block">
          <div className="flex h-16 items-center px-6">
            <Link href="/admin" className="text-lg font-bold">
              Aizorix <span className="text-brand-400">Admin</span>
            </Link>
          </div>
          <nav className="space-y-1 px-3 py-2 text-sm">
            <Link href="/admin" className="block rounded-lg px-3 py-2 hover:bg-slate-800">
              Overview
            </Link>
            <Link href="/admin#disputes" className="block rounded-lg px-3 py-2 hover:bg-slate-800">
              Disputes
            </Link>
            <Link href="/admin#fraud" className="block rounded-lg px-3 py-2 hover:bg-slate-800">
              Fraud queue
            </Link>
            <Link href="/marketplace" className="block rounded-lg px-3 py-2 hover:bg-slate-800">
              ← Exit admin
            </Link>
          </nav>
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <Topbar />
          <main className="flex-1 overflow-y-auto p-4 sm:p-6 lg:p-8">{children}</main>
        </div>
      </div>
    </RequireAuth>
  );
}
