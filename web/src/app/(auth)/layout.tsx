import Link from 'next/link';
import type { ReactNode } from 'react';

export default function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="grid min-h-screen lg:grid-cols-2">
      {/* Brand panel */}
      <div className="hidden flex-col justify-between bg-brand-600 p-12 text-white lg:flex">
        <Link href="/" className="text-2xl font-bold">
          Aizorix
        </Link>
        <div>
          <h2 className="text-3xl font-bold leading-tight">
            Work and pay with confidence.
          </h2>
          <p className="mt-4 max-w-md text-brand-100">
            Verified hourly tracking, encrypted screenshots, and escrow-backed
            payments — built into every contract.
          </p>
        </div>
        <p className="text-sm text-brand-200">© {new Date().getFullYear()} Aizorix, Inc.</p>
      </div>

      {/* Form panel */}
      <div className="flex items-center justify-center p-6">
        <div className="w-full max-w-md">{children}</div>
      </div>
    </div>
  );
}
