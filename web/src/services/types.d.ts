declare namespace Prism {
  type AccountStatus =
    | 'active'
    | 'expired'
    | 'quota_exhausted'
    | 'rate_limited'
    | 'refreshing'
    | 'disabled'
    | 'banned';

  type AccountProvider = 'openai' | 'custom';

  type Granularity = 'hour' | 'day' | 'month';

  type ModelSource = 'dynamic' | 'manual' | 'static';

  interface SuccessResponse {
    success: boolean;
  }

  interface Health {
    ok?: boolean;
    status?: string;
    service?: string;
    time?: string;
  }

  interface PoolSummary {
    active: number;
    expired: number;
    quota_exhausted: number;
    rate_limited: number;
    refreshing: number;
    disabled: number;
    banned: number;
    total: number;
  }

  interface AuthStatus {
    authenticated: boolean;
    proxy_api_key: string;
    pool: PoolSummary;
  }

  interface LoginStartResponse {
    authUrl: string;
    state: string;
    port: number;
    accountId?: string;
  }

  interface AccountUsage {
    request_count: number;
    input_tokens: number;
    output_tokens: number;
    empty_response_count: number;
    last_used_at?: string;
  }

  interface QuotaWindow {
    used_percent?: number;
    reset_at?: number;
    limit_window_seconds?: number;
  }

  interface QuotaRateLimit {
    allowed: boolean;
    limit_reached: boolean;
    window: QuotaWindow;
  }

  interface AccountQuota {
    plan_type: string;
    primary_rate_limit: QuotaRateLimit;
    secondary_rate_limit?: QuotaRateLimit;
  }

  interface Account {
    id: string;
    provider: AccountProvider;
    user_id?: string;
    account_id?: string;
    email?: string;
    enabled: boolean;
    plan_type?: string;
    status: AccountStatus;
    proxy_api_key: string;
    label?: string;
    custom_base_url?: string;
    custom_endpoint_type?: string;
    custom_user_agent?: string;
    custom_api_key_set?: boolean;
    usage: AccountUsage;
    quota?: AccountQuota;
    quota_fetched_at?: string;
    created_at: string;
    updated_at: string;
    expires_at?: string;
    last_refresh_at?: string;
    rate_limited_until?: string;
  }

  interface CustomAccountPayload {
    label?: string;
    plan_type?: string;
    custom_base_url: string;
    custom_api_key: string;
    custom_endpoint_type?: string;
    custom_user_agent?: string;
    enabled?: boolean;
  }

  interface AccountUpdatePayload {
    provider?: AccountProvider;
    user_id?: string;
    account_id?: string;
    email?: string;
    plan_type?: string;
    proxy_api_key?: string;
    label?: string;
    enabled?: boolean;
    custom_base_url?: string;
    custom_api_key?: string;
    custom_endpoint_type?: string;
    custom_user_agent?: string;
  }

  interface UsageSummary {
    total_input_tokens: number;
    total_output_tokens: number;
    total_cached_tokens: number;
    total_request_count: number;
    last_recorded_at?: string;
  }

  interface Overview {
    pool: PoolSummary;
    default_model: string;
    models_total: number;
    usage: UsageSummary;
  }

  interface UsageHistoryQuery {
    granularity?: Granularity;
    from?: string;
    to?: string;
  }

  interface UsageHistoryPoint {
    timestamp: string;
    input_tokens: number;
    output_tokens: number;
    cached_tokens: number;
    request_count: number;
    total_tokens: number;
  }

  interface UsageHistory {
    granularity: Granularity;
    from?: string;
    to?: string;
    points: UsageHistoryPoint[];
  }

  interface UsageEventsQuery {
    from?: string;
    to?: string;
    model?: string;
    account?: string;
    page?: number;
    page_size?: number;
  }

  interface UsageEvent {
    id: number;
    occurred_at: string;
    account_identity: string;
    account_id: string;
    account_provider: string;
    account_display_name: string;
    account_label: string;
    account_email: string;
    upstream_account_id: string;
    model_id: string;
    input_tokens: number;
    output_tokens: number;
    cached_tokens: number;
    total_tokens: number;
    request_count: number;
  }

  interface UsageEvents {
    items: UsageEvent[];
    page: number;
    page_size: number;
    total: number;
    total_pages: number;
    summary: UsageSummary;
  }

  interface RequestEventsQuery {
    from?: string;
    to?: string;
    model?: string;
    account?: string;
    source_path?: string;
    success?: boolean;
    page?: number;
    page_size?: number;
  }

  interface RequestEventSummary {
    total_request_count: number;
    success_request_count: number;
    failed_request_count: number;
    avg_duration_ms: number;
    avg_first_token_ms?: number;
    last_completed_at?: string;
  }

  interface RequestEvent {
    id: number;
    started_at: string;
    completed_at: string;
    duration_ms: number;
    first_token_ms?: number;
    success: boolean;
    status_code?: number;
    error_message?: string;
    source_path: string;
    endpoint_style: string;
    request_stream: boolean;
    retry_attempt: number;
    upstream_type: string;
    account_identity: string;
    account_id: string;
    account_provider: string;
    account_display_name: string;
    account_label: string;
    account_email: string;
    upstream_account_id: string;
    requested_model: string;
    routed_model: string;
    upstream_request_id?: string;
    response_id?: string;
    input_tokens?: number;
    output_tokens?: number;
    cached_tokens?: number;
    reasoning_tokens?: number;
  }

  interface RequestEvents {
    items: RequestEvent[];
    page: number;
    page_size: number;
    total: number;
    total_pages: number;
    summary: RequestEventSummary;
  }

  interface AccountUsageBreakdown {
    account_identity: string;
    account_id: string;
    account_provider: string;
    account_display_name: string;
    account_label: string;
    account_email: string;
    upstream_account_id: string;
    total_input_tokens: number;
    total_output_tokens: number;
    total_cached_tokens: number;
    total_request_count: number;
    last_recorded_at?: string;
  }

  interface ModelUsageBreakdown {
    model_id: string;
    total_input_tokens: number;
    total_output_tokens: number;
    total_cached_tokens: number;
    total_request_count: number;
    last_recorded_at?: string;
  }

  interface AccountModelUsageBreakdown {
    account_identity: string;
    account_id: string;
    account_provider: string;
    account_display_name: string;
    account_label: string;
    account_email: string;
    upstream_account_id: string;
    model_id: string;
    total_input_tokens: number;
    total_output_tokens: number;
    total_cached_tokens: number;
    total_request_count: number;
    last_recorded_at?: string;
  }

  interface Settings {
    proxy_api_key: string;
    default_model: string;
    default_reasoning_effort: string;
    inject_desktop_context: boolean;
    refresh_concurrency: number;
    request_interval_ms: number;
    max_concurrent_per_account: number;
    tier_priority?: string[];
    oauth_client_id: string;
    oauth_auth_endpoint: string;
    oauth_token_endpoint: string;
    openai_base_url: string;
    openai_originator: string;
    openai_user_agent: string;
    openai_client_version: string;
    client_version_last_checked?: string;
    client_version_last_updated?: string;
  }

  interface ReasoningEffort {
    reasoningEffort: string;
    description?: string;
  }

  interface ModelInfo {
    id: string;
    displayName: string;
    description?: string;
    object?: string;
    created?: number;
    owned_by?: string;
    isDefault?: boolean;
    supportedReasoningEfforts?: ReasoningEffort[];
    defaultReasoningEffort?: string;
    inputModalities?: string[];
    outputModalities?: string[];
    upgrade?: any;
  }

  interface OpenAIModel {
    id: string;
    object: string;
    created: number;
    owned_by: string;
  }

  interface ModelEntry extends ModelInfo {
    source?: ModelSource;
    record_id?: string;
    updated_at?: string;
  }

  interface AccountModelCatalog {
    account_id: string;
    account_email: string;
    client_version: string;
    fetched_at?: string;
    expires_at?: string;
    refresh_error?: string;
    used_stale_cache: boolean;
    dynamic_models: ModelEntry[];
    manual_models: ModelEntry[];
    models: ModelEntry[];
  }

  interface CustomAccountModelRecord {
    account_id: string;
    account_email?: string;
    account_label?: string;
    provider?: AccountProvider;
    model_id: string;
    display_name?: string;
    object?: string;
    owned_by?: string;
    created?: number;
    fetched_at?: string;
    expires_at?: string;
    updated_at?: string;
    last_error?: string;
  }

  interface ManualModelPayload {
    id: string;
    displayName?: string;
    description?: string;
    defaultReasoningEffort?: string;
    supportedReasoningEfforts?: string[];
    inputModalities?: string[];
    outputModalities?: string[];
  }

  interface ManualModelRecord extends ModelInfo {
    record_id: string;
    created_at: string;
    updated_at: string;
  }

  interface ModelMapping {
    record_id: string;
    model_name: string;
    target_model: string;
    apply_global: boolean;
    account_id?: string;
    account_email?: string;
    created_at: string;
    updated_at: string;
  }

  interface ModelMappingPayload {
    recordId?: string;
    modelName: string;
    targetModel: string;
    applyGlobal: boolean;
    accountId?: string;
  }

  interface IPRule {
    id: string;
    list_type: 'whitelist' | 'blacklist';
    value: string;
    match_type: 'ip' | 'cidr';
    created_at: string;
    updated_at: string;
  }

  interface AccessStat {
    ip: string;
    request_count: number;
    denied_count: number;
    last_seen_at?: string;
    last_allowed_at?: string;
    last_denied_at?: string;
    last_path?: string;
    last_method?: string;
  }

  interface SourceSummary {
    unique_ips: number;
    total_requests: number;
    last_seen_at?: string;
  }

  interface DeniedSummary {
    unique_ips: number;
    total_denied_count: number;
    last_denied_at?: string;
  }

  interface IPFilterOverview {
    enabled: boolean;
    updated_at?: string;
    whitelist_rules: IPRule[];
    blacklist_rules: IPRule[];
    top_sources: AccessStat[];
    top_denied: AccessStat[];
    source_summary: SourceSummary;
    denied_summary: DeniedSummary;
  }

  interface IPRulePayload {
    listType: 'whitelist' | 'blacklist';
    value: string;
  }
}
