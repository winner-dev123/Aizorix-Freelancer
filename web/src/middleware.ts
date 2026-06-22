import { NextResponse, type NextRequest } from 'next/server';

/**
 * Edge route protection.
 *
 * The access token lives only in browser memory and is invisible to the edge,
 * so middleware can't fully authorize a request. What it CAN see is the
 * httpOnly refresh cookie set by the gateway. We use its presence as a cheap
 * gate: no refresh cookie → definitely logged out → bounce to /login before any
 * dashboard JS loads. Fine-grained role checks happen client-side in
 * `RequireAuth` (and are always re-enforced by the API).
 *
 * Auth pages redirect the other way: if a session cookie exists, send the user
 * to their dashboard instead of showing login/register.
 */

const REFRESH_COOKIE = 'aizorix_refresh';

const PROTECTED_PREFIXES = [
  '/freelancer',
  '/client',
  '/marketplace',
  '/projects',
  '/proposals',
  '/contracts',
  '/messages',
  '/payments',
  '/admin',
];

const AUTH_PATHS = ['/login', '/register'];

export function middleware(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has(REFRESH_COOKIE);

  const isProtected = PROTECTED_PREFIXES.some(
    (p) => pathname === p || pathname.startsWith(`${p}/`),
  );
  const isAuthPage = AUTH_PATHS.includes(pathname);

  if (isProtected && !hasSession) {
    const url = req.nextUrl.clone();
    url.pathname = '/login';
    url.searchParams.set('next', pathname);
    return NextResponse.redirect(url);
  }

  if (isAuthPage && hasSession) {
    const url = req.nextUrl.clone();
    url.pathname = '/marketplace';
    url.search = '';
    return NextResponse.redirect(url);
  }

  return NextResponse.next();
}

export const config = {
  // Skip Next internals, API proxy, and static assets.
  matcher: ['/((?!_next/static|_next/image|api/gateway|favicon.ico|.*\\.).*)'],
};
