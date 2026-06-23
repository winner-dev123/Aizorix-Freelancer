/**
 * Shared domain types for the Aizorix marketplace frontend.
 *
 * These mirror the public REST surface in `api/openapi/gateway.yaml` and the
 * gRPC-derived resources it proxies. Money is always represented in integer
 * cents (`*_cents`) to avoid float drift, matching the backend contract.
 *
 * Naming follows the wire format (snake_case) so API responses can be used
 * directly without a transform layer. Helpers in `lib/format.ts` adapt these
 * for display.
 */

// ──────────────────────────────────────────────────────────────────────────
// Primitives & shared shapes
// ──────────────────────────────────────────────────────────────────────────

export type UUID = string;
/** RFC 3339 / ISO-8601 timestamp string. */
export type ISODateString = string;

export type AccountType = 'client' | 'freelancer';
export type UserRole = AccountType | 'admin';

/** Standard gateway error envelope (`components.schemas.Error`). */
export interface ApiError {
  code: string;
  message: string;
  /** Field-level validation messages, keyed by field name. */
  fields?: Record<string, string>;
}

/** Cursor-paginated list envelope used by browse/search endpoints. */
export interface Paginated<T> {
  items: T[];
  next_cursor?: string | null;
}

export type Currency = 'USD' | 'EUR' | 'GBP';

export type Money = {
  amount_cents: number;
  currency: Currency;
};

// ──────────────────────────────────────────────────────────────────────────
// Auth
// ──────────────────────────────────────────────────────────────────────────

/** `components.schemas.AuthTokens`. refresh_token is delivered as an httpOnly
 *  cookie in production; the field is kept optional for non-cookie flows. */
export interface AuthTokens {
  access_token: string;
  refresh_token?: string;
  access_expires_in: number;
  token_type: 'Bearer';
  user_id: UUID;
}

export interface RegisterRequest {
  email: string;
  password: string;
  account_type: AccountType;
  /** ISO 3166-1 alpha-2. */
  residency_country: string;
  accepted_terms: boolean;
  accepted_monitoring_disclosure?: boolean;
}

export interface LoginRequest {
  email: string;
  password: string;
  device_fingerprint?: string;
}

export interface AuthUser {
  id: UUID;
  email: string;
  role: UserRole;
  display_name: string;
  avatar_url?: string | null;
  /** Set true once KYC / identity verification has completed. */
  verified: boolean;
}

/** Raw shape returned by `GET /v1/auth/me`. The frontend maps this onto
 *  `AuthUser` in `authApi.me()` (account_type → role, email_verified →
 *  verified, etc.). */
export interface MeResponse {
  user_id: UUID;
  email: string;
  account_type: AccountType;
  roles: string[];
  email_verified: boolean;
  mfa_enabled: boolean;
}

// ──────────────────────────────────────────────────────────────────────────
// Projects & proposals
// ──────────────────────────────────────────────────────────────────────────

export type BudgetType = 'fixed' | 'hourly';
export type ProjectStatus = 'draft' | 'published' | 'in_progress' | 'closed';

/** `components.schemas.Project`. */
export interface Project {
  id: UUID;
  title: string;
  description: string;
  budget_type: BudgetType;
  budget_min_cents: number;
  budget_max_cents: number;
  currency: Currency;
  status: ProjectStatus;
  skills: string[];
  client_id?: UUID;
  proposals_count?: number;
  created_at?: ISODateString;
}

export interface ProjectSearchParams {
  q?: string;
  skills?: string[];
  budget_type?: BudgetType;
  min_budget?: number;
  cursor?: string;
  limit?: number;
}

export interface CreateProjectInput {
  title: string;
  description: string;
  budget_type: BudgetType;
  budget_min_cents: number;
  budget_max_cents: number;
  currency: Currency;
  skills: string[];
}

export type ProposalStatus = 'submitted' | 'shortlisted' | 'declined' | 'accepted' | 'withdrawn';

export interface Proposal {
  id: UUID;
  project_id: UUID;
  freelancer_id: UUID;
  cover_letter: string;
  /** Total bid (cents) for fixed projects; hourly rate (cents/hour) for hourly. */
  bid_amount_cents: number;
  currency: Currency;
  estimated_duration_days?: number | null;
  status: ProposalStatus;
  connects_spent: number;
}

export interface SubmitProposalInput {
  project_id: UUID;
  cover_letter: string;
  bid_amount_cents: number;
  currency: Currency;
  estimated_duration_days?: number;
}

// ──────────────────────────────────────────────────────────────────────────
// Contracts & milestones
// ──────────────────────────────────────────────────────────────────────────

export type ContractType = 'fixed' | 'hourly';
export type ContractStatus =
  | 'pending_funding'
  | 'active'
  | 'paused'
  | 'completed'
  | 'disputed'
  | 'cancelled';

export type MilestoneStatus = 'pending' | 'funded' | 'submitted' | 'approved' | 'released' | 'disputed';

export interface Milestone {
  /** Global milestone id; used to address approve/fund/submit endpoints. */
  id: UUID;
  /** Sequence number within the contract (display ordering). */
  seq: number;
  title: string;
  amount_cents: number;
  status: MilestoneStatus;
  due_at?: ISODateString | null;
  funded_at?: ISODateString | null;
  released_at?: ISODateString | null;
}

export interface Contract {
  id: UUID;
  project_id: UUID;
  proposal_id?: UUID;
  client_id: UUID;
  freelancer_id: UUID;
  budget_type: ContractType;
  status: ContractStatus;
  currency: Currency;
  /** Total contract value (fixed-price contracts). */
  total_amount_cents?: number | null;
  /** Agreed hourly rate (hourly contracts only). */
  hourly_rate_cents?: number | null;
  /** Weekly hour cap for hourly contracts. */
  weekly_hour_limit?: number | null;
  platform_fee_bps?: number;
  /** Present on the detail response (GET /v1/contracts/{id}); omitted in list responses. */
  milestones?: Milestone[];
  started_at?: ISODateString | null;
  ended_at?: ISODateString | null;
  end_reason?: string | null;
}

/** A single entry on the contract activity timeline (an event-sourced state transition,
 *  as returned by GET /v1/contracts/{id}/events). */
export interface ContractEvent {
  event: string;
  from_status?: string | null;
  to_status: string;
  actor_id?: string | null;
  payload?: Record<string, unknown>;
  created_at: ISODateString;
}

// ──────────────────────────────────────────────────────────────────────────
// Time tracking (verified hourly work)
// ──────────────────────────────────────────────────────────────────────────

export type WorkSessionStatus = 'active' | 'idle' | 'closed';

export interface WorkSession {
  id: UUID;
  contract_id: UUID;
  freelancer_id: UUID;
  status: WorkSessionStatus;
  started_at: ISODateString;
  ended_at?: ISODateString | null;
  /** Memo describing what was worked on. */
  memo?: string;
}

/** A 10-minute activity interval captured by the desktop tracker. */
export interface ActivityInterval {
  /** Interval start, aligned to 10-minute boundaries. */
  start: ISODateString;
  /** 0–100 activity percentage (keyboard + mouse signal). */
  activity_pct: number;
  /** Whether a screenshot exists for this interval. */
  has_screenshot: boolean;
  screenshot_id?: UUID;
  /** True if fraud/anomaly scoring flagged this interval. */
  flagged: boolean;
}

/** Aggregated billable time for one ISO week of a contract. */
export interface TimesheetWeek {
  contract_id: UUID;
  /** ISO week start (Monday), date-only. */
  week_start: ISODateString;
  total_seconds: number;
  /** Average activity across the week, 0–100. */
  avg_activity_pct: number;
  amount_cents: number;
  currency: Currency;
  status: 'open' | 'pending_review' | 'billed' | 'disputed';
  intervals: ActivityInterval[];
}

// ──────────────────────────────────────────────────────────────────────────
// Screenshots
// ──────────────────────────────────────────────────────────────────────────

export type ScreenshotFlag = 'none' | 'low_activity' | 'duplicate' | 'manual_review' | 'blocked';

export interface Screenshot {
  id: UUID;
  contract_id: UUID;
  session_id: UUID;
  captured_at: ISODateString;
  activity_pct: number;
  flag: ScreenshotFlag;
  /** True until the authorized, audited decrypt-on-read URL is fetched. */
  encrypted: boolean;
  memo?: string;
}

/** Response of GET /v1/screenshots/{id}: a short-lived signed URL plus the
 *  client-side material needed to decrypt the object. */
export interface AuthorizedScreenshot extends Screenshot {
  signed_url: string;
  /** Base64 data key, itself decrypted server-side via KMS for the requester. */
  decryption_key: string;
  /** AES-GCM nonce (base64). */
  nonce: string;
  expires_at: ISODateString;
}

// ──────────────────────────────────────────────────────────────────────────
// Payments
// ──────────────────────────────────────────────────────────────────────────

export type PaymentDirection = 'charge' | 'payout' | 'refund' | 'fee';
export type PaymentStatus = 'pending' | 'processing' | 'succeeded' | 'failed' | 'reversed';

export interface PaymentTransaction {
  id: UUID;
  contract_id?: UUID;
  direction: PaymentDirection;
  status: PaymentStatus;
  amount_cents: number;
  currency: Currency;
  description: string;
  created_at: ISODateString;
}

export interface PaymentSummary {
  available_cents: number;
  pending_cents: number;
  in_escrow_cents: number;
  lifetime_cents: number;
  currency: Currency;
}

// ──────────────────────────────────────────────────────────────────────────
// Messaging
// ──────────────────────────────────────────────────────────────────────────

export interface MessageAttachment {
  id: UUID;
  filename: string;
  url: string;
  size_bytes: number;
  content_type: string;
}

export interface Message {
  id: UUID;
  conversation_id: UUID;
  sender_id: UUID;
  /** Null for non-text messages (e.g. system/file kinds). */
  body?: string | null;
  kind: string;
  created_at: ISODateString;
  edited_at?: ISODateString | null;
}

/** A conversation, as returned by GET /v1/messaging/conversations. The backend does not
 *  (yet) enrich this with participant names, unread counts, or a last-message preview —
 *  those would need a server-side join against the user service + message aggregation. */
export interface MessageThread {
  id: UUID;
  contract_id?: UUID | null;
  project_id?: UUID | null;
  subject?: string | null;
  last_message_at?: ISODateString | null;
  created_at: ISODateString;
}
