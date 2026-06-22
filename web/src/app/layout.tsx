import type { Metadata } from 'next';
import { Inter } from 'next/font/google';

import { Providers } from '@/app/providers';

import './globals.css';

const inter = Inter({ subsets: ['latin'], variable: '--font-inter' });

const siteUrl = process.env.NEXT_PUBLIC_SITE_URL ?? 'http://localhost:3000';

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),
  title: {
    default: 'Aizorix — Hire & work with verified hourly tracking',
    template: '%s · Aizorix',
  },
  description:
    'Aizorix is a global freelancer marketplace with verified hourly work: encrypted screenshots, real activity tracking, and escrow-backed payments.',
  openGraph: { type: 'website', siteName: 'Aizorix', url: siteUrl },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={inter.variable}>
      <body className="min-h-screen font-sans">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
