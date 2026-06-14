import { Tag } from 'antd';

export const numberFormat = new Intl.NumberFormat('zh-CN');

export const compactNumberFormat = new Intl.NumberFormat('zh-CN', {
  notation: 'compact',
  maximumFractionDigits: 1,
});

const ONE_HUNDRED_MILLION = 100_000_000;

export const formatNumber = (value?: number) => numberFormat.format(value ?? 0);

export const formatCompactNumber = (value?: number) => {
  const normalizedValue = value ?? 0;
  if (Math.abs(normalizedValue) >= ONE_HUNDRED_MILLION) {
    return `${(normalizedValue / ONE_HUNDRED_MILLION).toFixed(4)} 亿`;
  }
  return compactNumberFormat.format(normalizedValue);
};

export const formatDateTime = (value?: string | number | null) => {
  if (!value) return '-';
  const date =
    typeof value === 'number'
      ? new Date(value > 10_000_000_000 ? value : value * 1000)
      : new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return date.toLocaleString('zh-CN', {
    hour12: false,
  });
};

export const formatPercent = (value?: number | null) => {
  if (value === undefined || value === null) return '-';
  return `${value.toFixed(1)}%`;
};

export const maskSecret = (value?: string, prefix = 6, suffix = 4) => {
  if (!value) return '-';
  if (value.length <= prefix + suffix) return `${value.slice(0, 2)}***`;
  return `${value.slice(0, prefix)}...${value.slice(-suffix)}`;
};

export const maskEmail = (value?: string): string => {
  if (!value) return '-';
  const [localPart, domain] = value.split('@');
  if (!localPart || !domain) {
    return `${value.slice(0, Math.min(2, value.length))}**`;
  }
  if (localPart.length <= 2) {
    return `${localPart.slice(0, 1)}**@${domain}`;
  }
  const suffix = localPart.length > 4 ? localPart.slice(-1) : '';
  return `${localPart.slice(0, 2)}**${suffix}@${domain}`;
};

export const maskUsageAccountDisplay = (item?: {
  account_provider?: string;
  account_display_name?: string;
  account_label?: string;
  account_email?: string;
}) => {
  if (!item) return '-';
  const displayName = item.account_display_name || '-';
  const email = item.account_email || '';
  const shouldMaskEmail =
    item.account_provider === 'openai' &&
    !item.account_label &&
    !!email &&
    displayName === email;
  return shouldMaskEmail ? maskEmail(email) : displayName;
};

export const displayAccount = (account?: Partial<Prism.Account>) => {
  if (!account) return '-';
  return (
    account.label ||
    account.email ||
    account.account_id ||
    account.custom_base_url ||
    account.id ||
    '-'
  );
};

const statusMeta: Record<Prism.AccountStatus, { color: string; text: string }> =
  {
    active: { color: 'success', text: '活跃' },
    expired: { color: 'warning', text: '过期' },
    quota_exhausted: { color: 'error', text: '额度耗尽' },
    rate_limited: { color: 'orange', text: '限流' },
    refreshing: { color: 'processing', text: '刷新中' },
    disabled: { color: 'default', text: '停用' },
    banned: { color: 'error', text: '封禁' },
  };

export const renderStatusTag = (status?: Prism.AccountStatus) => {
  if (!status) return <Tag>未知</Tag>;
  const meta = statusMeta[status] || { color: 'default', text: status };
  return <Tag color={meta.color}>{meta.text}</Tag>;
};

export const renderProviderTag = (provider?: string) => {
  if (provider === 'custom') return <Tag color="purple">Custom</Tag>;
  return <Tag color="blue">OpenAI</Tag>;
};

export const sourceColor = (source?: Prism.ModelSource) => {
  if (source === 'dynamic') return 'blue';
  if (source === 'manual') return 'purple';
  return 'default';
};

export const toRFC3339 = (value?: string | number | Date | null) => {
  if (!value) return undefined;
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  return date.toISOString();
};

export const jsonStringify = (value: any) => JSON.stringify(value, null, 2);
