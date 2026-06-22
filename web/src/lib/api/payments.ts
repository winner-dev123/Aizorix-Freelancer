import { get, post } from '@/lib/api/client';
import type { Paginated, PaymentSummary, PaymentTransaction, UUID } from '@/lib/types';

/** Payment service — wraps /v1/payments. */
export const paymentsApi = {
  /** Wallet/escrow balance summary for the current user. */
  summary(): Promise<PaymentSummary> {
    return get<PaymentSummary>('/v1/payments/summary');
  },

  /** Paginated ledger of charges, payouts, refunds and fees. */
  transactions(cursor?: string): Promise<Paginated<PaymentTransaction>> {
    return get<Paginated<PaymentTransaction>>('/v1/payments/transactions', {
      params: { cursor },
    });
  },

  /** Create a Stripe SetupIntent / onboarding link for payouts (Connect). */
  startPayoutOnboarding(): Promise<{ onboarding_url: string }> {
    return post<{ onboarding_url: string }>('/v1/payments/connect/onboard', {});
  },

  /** Request a payout of available balance to the connected account. */
  requestPayout(amountCents: number): Promise<PaymentTransaction> {
    return post<PaymentTransaction>('/v1/payments/payouts', { amount_cents: amountCents });
  },

  /** Fund a milestone's escrow (client). */
  fundMilestone(contractId: UUID, seq: number): Promise<PaymentTransaction> {
    return post<PaymentTransaction>('/v1/payments/escrow/fund', {
      contract_id: contractId,
      milestone_seq: seq,
    });
  },
};
