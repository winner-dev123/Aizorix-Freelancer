'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';

import { useAuth } from '@/hooks/useAuth';
import { cn } from '@/lib/utils';
import type { UserRole } from '@/lib/types';

interface NavItem {
  href: string;
  label: string;
  icon: string;
  /** Roles allowed to see the item; omitted = all authenticated users. */
  roles?: UserRole[];
}

const NAV: NavItem[] = [
  { href: '/marketplace', label: 'Marketplace', icon: '🔎' },
  { href: '/freelancer', label: 'Freelancer', icon: '💼', roles: ['freelancer'] },
  { href: '/client', label: 'Client', icon: '🏢', roles: ['client'] },
  { href: '/messages', label: 'Messages', icon: '💬' },
  { href: '/payments', label: 'Payments', icon: '💳' },
  { href: '/admin', label: 'Admin', icon: '🛡️', roles: ['admin'] },
];

export function Sidebar() {
  const pathname = usePathname();
  const { user } = useAuth();
  const role = user?.role;

  const items = NAV.filter((item) => !item.roles || (role && item.roles.includes(role)));

  return (
    <aside className="hidden w-60 shrink-0 border-r border-slate-200 bg-white md:block">
      <div className="flex h-16 items-center px-6">
        <Link href="/marketplace" className="text-lg font-bold text-brand-600">
          Aizorix
        </Link>
      </div>
      <nav className="space-y-1 px-3 py-2">
        {items.map((item) => {
          const active = pathname.startsWith(item.href);
          return (
            <Link
              key={item.href}
              href={item.href}
              className={cn(
                'flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors',
                active
                  ? 'bg-brand-50 text-brand-700'
                  : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900',
              )}
            >
              <span aria-hidden>{item.icon}</span>
              {item.label}
            </Link>
          );
        })}
      </nav>
    </aside>
  );
}
