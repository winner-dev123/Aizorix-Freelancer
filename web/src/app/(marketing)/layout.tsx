import Link from 'next/link';
import type { ReactNode } from 'react';

import { Button } from '@/components/ui/Button';

export default function MarketingLayout({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col">
      <header className="border-b border-slate-200 bg-white/80 backdrop-blur">
        <div className="container-page flex h-16 items-center justify-between">
          <Link href="/" className="text-xl font-bold text-brand-600">
            Aizorix
          </Link>
          <nav className="flex items-center gap-2">
            <Link href="/login">
              <Button variant="ghost" size="sm">
                Sign in
              </Button>
            </Link>
            <Link href="/register">
              <Button size="sm">Get started</Button>
            </Link>
          </nav>
        </div>
      </header>
      <main className="flex-1">{children}</main>
      <footer className="border-t border-slate-200 bg-white py-8">
        <div className="container-page flex flex-col items-center justify-between gap-4 text-sm text-muted sm:flex-row">
          <span>© {new Date().getFullYear()} Aizorix, Inc.</span>
          <div className="flex gap-4">
            <Link href="/login" className="hover:text-slate-900">
              Sign in
            </Link>
            <a href="#features" className="hover:text-slate-900">
              Features
            </a>
          </div>
        </div>
      </footer>
    </div>
  );
}
