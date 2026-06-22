import { get } from '@/lib/api/client';
import type { AuthorizedScreenshot, Screenshot, UUID } from '@/lib/types';

/** Screenshot service — wraps /v1/screenshots. */
export const screenshotsApi = {
  /** List screenshot metadata for a contract within a time window.
   *  Thumbnails are NOT returned here — only encrypted metadata. */
  listForContract(
    contractId: UUID,
    params: { from?: string; to?: string; session_id?: UUID } = {},
  ): Promise<Screenshot[]> {
    return get<Screenshot[]>('/v1/screenshots', {
      params: { contract_id: contractId, ...params },
    });
  },

  /**
   * Fetch an authorized, audited screenshot: a short-lived signed URL plus the
   * decryption material. Every call is logged server-side (decrypt-on-read);
   * 403 if the caller is not a party to the contract.
   */
  authorize(id: UUID): Promise<AuthorizedScreenshot> {
    return get<AuthorizedScreenshot>(`/v1/screenshots/${id}`);
  },
};
