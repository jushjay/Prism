import { request } from '@umijs/max';

const jsonHeaders = {
  'Content-Type': 'application/json',
};

const cleanParams = (params: Record<string, any> = {}) =>
  Object.fromEntries(
    Object.entries(params).filter(
      ([, value]) => value !== undefined && value !== '',
    ),
  );

export async function health() {
  return request<Prism.Health>('/health');
}

export async function getAuthStatus() {
  return request<Prism.AuthStatus>('/auth/status');
}

export async function loginStart(accountId?: string) {
  return request<Prism.LoginStartResponse>('/auth/login-start', {
    method: 'POST',
    headers: jsonHeaders,
    data: accountId ? { accountId } : undefined,
  });
}

export async function codeRelay(callbackUrl: string) {
  return request<Prism.SuccessResponse & { account?: Prism.Account }>(
    '/auth/code-relay',
    {
      method: 'POST',
      headers: jsonHeaders,
      data: { callbackUrl },
    },
  );
}

export async function importCLI(auth_json: string) {
  return request<Prism.SuccessResponse & { account?: Prism.Account }>(
    '/auth/import-cli',
    {
      method: 'POST',
      headers: jsonHeaders,
      data: { auth_json },
    },
  );
}

export async function importToken(token: string, refresh_token: string) {
  return request<Prism.SuccessResponse & { account?: Prism.Account }>(
    '/auth/token',
    {
      method: 'POST',
      headers: jsonHeaders,
      data: { token, refresh_token },
    },
  );
}

export async function dashboardLogin(password: string) {
  return request<Prism.SuccessResponse>('/auth/dashboard-login', {
    method: 'POST',
    headers: jsonHeaders,
    data: { password },
  });
}

export async function dashboardLogout() {
  return request<Prism.SuccessResponse>('/auth/dashboard-logout', {
    method: 'POST',
  });
}

export async function dashboardStatus() {
  return request<{ authenticated: boolean }>('/auth/dashboard-status');
}

export async function listAccounts() {
  return request<Prism.Account[]>('/auth/accounts');
}

export async function createCustomAccount(data: Prism.CustomAccountPayload) {
  return request<Prism.SuccessResponse & { account: Prism.Account }>(
    '/auth/accounts/custom',
    {
      method: 'POST',
      headers: jsonHeaders,
      data,
    },
  );
}

export async function updateAccount(
  id: string,
  data: Prism.AccountUpdatePayload,
) {
  return request<Prism.SuccessResponse & { account: Prism.Account }>(
    `/auth/accounts/${id}`,
    {
      method: 'PUT',
      headers: jsonHeaders,
      data,
    },
  );
}

export async function deleteAccount(id: string) {
  return request<Prism.SuccessResponse>(`/auth/accounts/${id}`, {
    method: 'DELETE',
  });
}

export async function setAccountEnabled(id: string, enabled: boolean) {
  return request<Prism.SuccessResponse & { account: Prism.Account }>(
    `/auth/accounts/${id}/enabled`,
    {
      method: 'POST',
      headers: jsonHeaders,
      data: { enabled },
    },
  );
}

export async function resetAccountStatus(id: string) {
  return request<Prism.SuccessResponse & { account: Prism.Account }>(
    `/auth/accounts/${id}/reset-status`,
    {
      method: 'POST',
    },
  );
}

export async function refreshAccount(id: string) {
  return request<Prism.SuccessResponse & { account: Prism.Account }>(
    `/auth/accounts/${id}/refresh`,
    {
      method: 'POST',
    },
  );
}

export async function getOverview() {
  return request<Prism.Overview>('/admin/overview');
}

export async function getUsageSummary() {
  return request<Prism.UsageSummary>('/admin/usage/summary');
}

export async function getUsageHistory(params?: Prism.UsageHistoryQuery) {
  return request<Prism.UsageHistory>('/admin/usage/history', {
    params: cleanParams(params),
  });
}

export async function getUsageEvents(params?: Prism.UsageEventsQuery) {
  return request<Prism.UsageEvents>('/admin/usage/events', {
    params: cleanParams(params),
  });
}

export async function getRequestEventSummary(
  params?: Prism.RequestEventsQuery,
) {
  return request<Prism.RequestEventSummary>('/admin/request-events/summary', {
    params: cleanParams(params),
  });
}

export async function getRequestEvents(params?: Prism.RequestEventsQuery) {
  return request<Prism.RequestEvents>('/admin/request-events', {
    params: cleanParams(params),
  });
}

export async function getUsageByAccounts() {
  return request<Prism.AccountUsageBreakdown[]>(
    '/admin/usage/breakdown/accounts',
  );
}

export async function getUsageByModels() {
  return request<Prism.ModelUsageBreakdown[]>('/admin/usage/breakdown/models');
}

export async function getUsageByAccountModels() {
  return request<Prism.AccountModelUsageBreakdown[]>(
    '/admin/usage/breakdown/account-models',
  );
}

export async function getSettings() {
  return request<Prism.Settings>('/admin/settings');
}

export async function getModelCatalog() {
  return request<Prism.ModelInfo[]>('/v1/models/catalog');
}

export async function getOpenAIModels() {
  return request<{ object: string; data: Prism.OpenAIModel[] }>('/v1/models');
}

export async function getAccountModels(accountId: string) {
  return request<Prism.AccountModelCatalog>(
    `/admin/models/accounts/${accountId}`,
  );
}

export async function listCustomAccountModels(params?: {
  account_id?: string;
  model?: string;
}) {
  return request<Prism.CustomAccountModelRecord[]>('/admin/models/custom', {
    params: cleanParams(params),
  });
}

export async function refreshAccountModels(accountId: string) {
  return request<Prism.AccountModelCatalog>(
    `/admin/models/accounts/${accountId}/refresh`,
    { method: 'POST' },
  );
}

export async function upsertManualModel(data: Prism.ManualModelPayload) {
  return request<Prism.SuccessResponse & { model: Prism.ManualModelRecord }>(
    '/admin/models/manual',
    {
      method: 'POST',
      headers: jsonHeaders,
      data,
    },
  );
}

export async function listModelMappings(account_id?: string) {
  return request<Prism.ModelMapping[]>('/admin/models/mappings', {
    params: cleanParams({ account_id }),
  });
}

export async function upsertModelMapping(data: Prism.ModelMappingPayload) {
  return request<Prism.SuccessResponse & { mapping: Prism.ModelMapping }>(
    '/admin/models/mappings',
    {
      method: 'POST',
      headers: jsonHeaders,
      data,
    },
  );
}

export async function updateModelMapping(
  id: string,
  data: Omit<Prism.ModelMappingPayload, 'recordId'>,
) {
  return request<Prism.SuccessResponse & { mapping: Prism.ModelMapping }>(
    `/admin/models/mappings/${id}`,
    {
      method: 'PUT',
      headers: jsonHeaders,
      data,
    },
  );
}

export async function deleteModelMapping(id: string) {
  return request<Prism.SuccessResponse>(`/admin/models/mappings/${id}`, {
    method: 'DELETE',
  });
}

export async function getIPFilter() {
  return request<Prism.IPFilterOverview>('/admin/security/ip-filter');
}

export async function setIPFilterEnabled(enabled: boolean) {
  return request<Prism.SuccessResponse & { enabled: boolean }>(
    '/admin/security/ip-filter',
    {
      method: 'POST',
      headers: jsonHeaders,
      data: { enabled },
    },
  );
}

export async function upsertIPRule(data: Prism.IPRulePayload) {
  return request<Prism.SuccessResponse & { rule: Prism.IPRule }>(
    '/admin/security/ip-rules',
    {
      method: 'POST',
      headers: jsonHeaders,
      data,
    },
  );
}

export async function deleteIPRule(id: string) {
  return request<Prism.SuccessResponse>(`/admin/security/ip-rules/${id}`, {
    method: 'DELETE',
  });
}
