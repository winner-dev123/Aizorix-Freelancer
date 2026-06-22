import Link from 'next/link';

import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';

const features = [
  {
    icon: '🔒',
    title: 'Verified hourly work',
    body: 'A desktop tracker captures encrypted screenshots every 15 minutes and measures real activity — so clients pay for work that actually happened.',
  },
  {
    icon: '🛡️',
    title: 'Escrow-backed payments',
    body: 'Milestones are funded into escrow up front and released on approval. Hourly weeks are auto-billed from verified time.',
  },
  {
    icon: '⚖️',
    title: 'Fraud detection built in',
    body: 'Activity anomalies and duplicate frames are flagged automatically and surfaced in the review grid for dispute resolution.',
  },
  {
    icon: '🌍',
    title: 'Global by design',
    body: 'Multi-currency contracts, KYC, and Stripe Connect payouts in 40+ countries.',
  },
];

export default function LandingPage() {
  return (
    <>
      <section className="container-page py-20 text-center sm:py-28">
        <span className="inline-flex rounded-full bg-brand-50 px-3 py-1 text-sm font-medium text-brand-700">
          The verified-work marketplace
        </span>
        <h1 className="mx-auto mt-6 max-w-3xl text-4xl font-extrabold tracking-tight text-slate-900 sm:text-5xl">
          Hire freelancers and pay only for work you can{' '}
          <span className="text-brand-600">verify</span>.
        </h1>
        <p className="mx-auto mt-5 max-w-2xl text-lg text-muted">
          Aizorix combines an Upwork-class marketplace with cryptographically
          verified hourly tracking and escrow — trust, without the guesswork.
        </p>
        <div className="mt-8 flex justify-center gap-3">
          <Link href="/register">
            <Button size="lg">Get started free</Button>
          </Link>
          <Link href="/marketplace">
            <Button size="lg" variant="outline">
              Browse projects
            </Button>
          </Link>
        </div>
      </section>

      <section id="features" className="container-page pb-24">
        <div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
          {features.map((f) => (
            <Card key={f.title} className="p-6">
              <div className="text-2xl" aria-hidden>
                {f.icon}
              </div>
              <h3 className="mt-3 font-semibold text-slate-900">{f.title}</h3>
              <p className="mt-2 text-sm text-muted">{f.body}</p>
            </Card>
          ))}
        </div>
      </section>
    </>
  );
}
