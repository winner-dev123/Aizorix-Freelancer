'use client';

import { Avatar } from '@/components/ui/Avatar';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { useAuth } from '@/hooks/useAuth';

export function Topbar() {
  const { user, logout } = useAuth();

  return (
    <header className="flex h-16 items-center justify-between border-b border-slate-200 bg-white px-4 sm:px-6">
      <div className="md:hidden">
        <span className="text-lg font-bold text-brand-600">Aizorix</span>
      </div>
      <div className="ml-auto flex items-center gap-4">
        {user && (
          <>
            <Badge tone={user.verified ? 'success' : 'warning'} dot>
              {user.verified ? 'Verified' : 'Unverified'}
            </Badge>
            <div className="flex items-center gap-2">
              <Avatar name={user.display_name} src={user.avatar_url} size={32} />
              <div className="hidden text-sm sm:block">
                <p className="font-medium text-slate-900">{user.display_name}</p>
                <p className="text-xs capitalize text-muted">{user.role}</p>
              </div>
            </div>
            <Button variant="ghost" size="sm" onClick={() => logout()}>
              Sign out
            </Button>
          </>
        )}
      </div>
    </header>
  );
}
