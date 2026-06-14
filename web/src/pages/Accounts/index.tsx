import { useRawRequest } from '@/hooks/useRawRequest';
import {
  codeRelay,
  createCustomAccount,
  deleteAccount,
  getAuthStatus,
  importCLI,
  importToken,
  listAccounts,
  loginStart,
  refreshAccount,
  resetAccountStatus,
  setAccountEnabled,
  updateAccount,
} from '@/services/api';
import {
  displayAccount,
  formatDateTime,
  formatNumber,
  formatPercent,
  maskEmail,
  maskSecret,
  renderProviderTag,
  renderStatusTag,
} from '@/utils/prism';
import {
  DeleteOutlined,
  EditOutlined,
  ExportOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  KeyOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import type { ActionType, ProColumns } from '@ant-design/pro-components';
import {
  ModalForm,
  PageContainer,
  ProCard,
  ProDescriptions,
  ProFormDependency,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
  ProFormTextArea,
  ProTable,
} from '@ant-design/pro-components';
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Popconfirm,
  Progress,
  Space,
  Switch,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import styles from './index.less';

const poolLabels: Array<[keyof Prism.PoolSummary, string]> = [
  ['active', '活跃'],
  ['expired', '过期'],
  ['quota_exhausted', '额度耗尽'],
  ['rate_limited', '限流'],
  ['refreshing', '刷新中'],
  ['disabled', '停用'],
  ['banned', '封禁'],
  ['total', '总数'],
];

const CUSTOM_RESPONSES_ENDPOINT = '/v1/responses';
const CUSTOM_CHAT_COMPLETIONS_ENDPOINT = '/v1/chat/completions';
type CustomEndpointMode = 'responses' | 'chat_completions' | 'custom';

function normalizeCustomEndpointPath(value?: string): string {
  const raw = String(value || '').trim();
  if (!raw) return CUSTOM_RESPONSES_ENDPOINT;
  let path = raw;
  if (/^https?:\/\//i.test(raw)) {
    try {
      path = new URL(raw).pathname || raw;
    } catch {
      path = raw;
    }
  }
  if (!path.startsWith('/')) path = `/${path}`;
  return `/${path
    .trim()
    .replace(/^\/+|\/+$/g, '')
    .toLowerCase()}`;
}

function customEndpointMode(value?: string): CustomEndpointMode {
  const normalized = normalizeCustomEndpointPath(value);
  if (normalized === CUSTOM_RESPONSES_ENDPOINT) return 'responses';
  if (normalized === CUSTOM_CHAT_COMPLETIONS_ENDPOINT)
    return 'chat_completions';
  return 'custom';
}

function customModelsEndpointPath(value?: string): string {
  const normalized = normalizeCustomEndpointPath(value);
  if (normalized.endsWith('/chat/completions')) {
    return `${normalized.slice(0, -'/chat/completions'.length)}/models`;
  }
  if (normalized.endsWith('/responses')) {
    return `${normalized.slice(0, -'/responses'.length)}/models`;
  }
  const segments = normalized.split('/').filter(Boolean);
  if (segments.length <= 1) return '/v1/models';
  return `/${[...segments.slice(0, -1), 'models'].join('/')}`;
}

function customEndpointValueFromForm(values: any): string {
  switch (values.custom_endpoint_mode as CustomEndpointMode) {
    case 'chat_completions':
      return CUSTOM_CHAT_COMPLETIONS_ENDPOINT;
    case 'responses':
      return CUSTOM_RESPONSES_ENDPOINT;
    default:
      return normalizeCustomEndpointPath(
        values.custom_endpoint_custom_path || values.custom_endpoint_type,
      );
  }
}

function customAccountFormValues(account?: Prism.Account) {
  const endpoint = account?.custom_endpoint_type || CUSTOM_RESPONSES_ENDPOINT;
  const mode = customEndpointMode(endpoint);
  return {
    ...account,
    custom_endpoint_mode: mode,
    custom_endpoint_custom_path: mode === 'custom' ? endpoint : undefined,
  };
}

type AccountLabelProps = {
  account?: Partial<Prism.Account>;
  onClick?: () => void;
};

const AccountLabel: React.FC<AccountLabelProps> = ({ account, onClick }) => {
  const [revealed, setRevealed] = useState(false);

  if (!account) {
    return <>-</>;
  }

  const displayValue = displayAccount(account);
  const shouldMaskEmail =
    account.provider === 'openai' &&
    !account.label &&
    !!account.email &&
    displayValue === account.email;
  const text =
    shouldMaskEmail && !revealed ? maskEmail(account.email) : displayValue;

  const content = onClick ? (
    <Typography.Link onClick={onClick}>{text}</Typography.Link>
  ) : (
    <Typography.Text>{text}</Typography.Text>
  );

  if (!shouldMaskEmail) {
    return content;
  }

  return (
    <Space size={4}>
      {content}
      <Tooltip title={revealed ? '隐藏完整邮箱' : '查看完整邮箱'}>
        <Button
          type="text"
          size="small"
          icon={revealed ? <EyeInvisibleOutlined /> : <EyeOutlined />}
          onClick={(event) => {
            event.stopPropagation();
            setRevealed((value) => !value);
          }}
        />
      </Tooltip>
    </Space>
  );
};

function formatQuotaWindowLabel(value?: number, fallback = '窗口'): string {
  if (!value || value <= 0) {
    return fallback;
  }
  if (value % (24 * 60 * 60) === 0) {
    const days = value / (24 * 60 * 60);
    return days === 7 ? '7天额度' : `${days}d 额度`;
  }
  if (value % (60 * 60) === 0) {
    const hours = value / (60 * 60);
    return hours === 5 ? '5h 额度' : `${hours}h 额度`;
  }
  if (value % 60 === 0) {
    return `${value / 60}m 额度`;
  }
  return `${value}s 额度`;
}

function renderQuotaWindow(
  rateLimit?: Prism.QuotaRateLimit,
  fallbackLabel?: string,
) {
  if (!rateLimit) {
    return '-';
  }

  const percentUsed = rateLimit.window.used_percent;
  const percentRemaining =
    typeof percentUsed === 'number'
      ? Number(Math.max(0, 100 - percentUsed).toFixed(1))
      : undefined;
  const statusText = !rateLimit.allowed
    ? '不可用'
    : rateLimit.limit_reached
    ? '已达上限'
    : '可用';
  const statusColor = !rateLimit.allowed
    ? 'default'
    : rateLimit.limit_reached
    ? 'error'
    : 'processing';

  return (
    <div className={styles.quotaBlock}>
      <Space size={[8, 8]} wrap>
        <Tag color={statusColor}>{statusText}</Tag>
        <Typography.Text type="secondary">
          {formatQuotaWindowLabel(
            rateLimit.window.limit_window_seconds,
            fallbackLabel || '额度',
          )}
        </Typography.Text>
      </Space>
      {percentRemaining !== undefined ? (
        <Progress
          percent={percentRemaining}
          size="small"
          strokeColor={
            rateLimit.limit_reached
              ? '#ff4d4f'
              : percentRemaining <= 20
              ? '#faad14'
              : '#1677ff'
          }
        />
      ) : (
        <Typography.Text type="secondary">暂无可用比例</Typography.Text>
      )}
      <Space direction="vertical" size={2}>
        <Typography.Text type="secondary">
          已用 {formatPercent(percentUsed)}
        </Typography.Text>
        <Typography.Text type="secondary">
          Reset At {formatDateTime(rateLimit.window.reset_at)}
        </Typography.Text>
      </Space>
    </div>
  );
}

function renderDisabledQuotaAlert() {
  return <Alert showIcon type="info" message="账号已停用" />;
}

const AccountsPage: React.FC = () => {
  const actionRef = useRef<ActionType>();
  const [current, setCurrent] = useState<Prism.Account>();
  const [editOpen, setEditOpen] = useState(false);
  const [customOpen, setCustomOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [importMode, setImportMode] = useState<'cli' | 'token'>('cli');
  const [oauthOpen, setOauthOpen] = useState(false);
  const [oauthSession, setOauthSession] = useState<Prism.LoginStartResponse>();
  const [oauthTargetAccount, setOauthTargetAccount] = useState<Prism.Account>();

  const authReq = useRawRequest(getAuthStatus);

  const reload = () => {
    actionRef.current?.reload();
    authReq.refresh();
  };

  const openAccountDetail = async (account: Prism.Account) => {
    setCurrent(account);
  };

  const handleResetStatus = async (account: Prism.Account) => {
    try {
      const result = await resetAccountStatus(account.id);
      message.success('账号状态已重置为活跃');
      if (current?.id === account.id) {
        setCurrent(result.account);
      }
      reload();
    } catch (error) {
      message.error('重置账号状态失败');
    }
  };

  const startOAuth = async (account?: Prism.Account) => {
    const result = await loginStart(account?.id);
    setOauthSession(result);
    setOauthTargetAccount(account);
    setOauthOpen(true);
    window.open(result.authUrl, 'prism-oauth', 'width=720,height=760');
  };

  useEffect(() => {
    const handler = (event: MessageEvent) => {
      if (event.data?.type === 'oauth-callback-success') {
        message.success(
          oauthTargetAccount ? '账号重新认证完成' : 'OAuth 登录完成',
        );
        setOauthOpen(false);
        setOauthSession(undefined);
        setOauthTargetAccount(undefined);
        setEditOpen(false);
        setCurrent(undefined);
        reload();
      }
      if (event.data?.type === 'oauth-callback-error') {
        message.error(event.data?.error || 'OAuth 登录失败');
      }
    };
    window.addEventListener('message', handler);
    return () => window.removeEventListener('message', handler);
  }, [oauthTargetAccount]);

  const columns: ProColumns<Prism.Account>[] = [
    {
      title: '账号',
      dataIndex: 'email',
      width: 260,
      render: (_, record) => (
        <Space direction="vertical" size={2}>
          <Space>
            {renderProviderTag(record.provider)}
            {renderStatusTag(record.status)}
          </Space>
          <AccountLabel
            account={record}
            onClick={() => openAccountDetail(record)}
          />
          <Typography.Text type="secondary" copyable={{ text: record.id }}>
            {record.id.slice(0, 8)}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: '套餐',
      dataIndex: 'plan_type',
      width: 120,
      render: (_, record) => record.plan_type || '-',
      valueType: 'select',
      valueEnum: {
        plus: { text: 'plus' },
        pro: { text: 'pro' },
        free: { text: 'free' },
        custom: { text: 'custom' },
      },
    },
    {
      title: '启用',
      dataIndex: 'enabled',
      width: 92,
      hideInSearch: true,
      render: (_, record) => (
        <Switch
          checked={record.enabled}
          size="small"
          onChange={async (checked) => {
            await setAccountEnabled(record.id, checked);
            message.success(checked ? '账号已启用' : '账号已停用');
            reload();
          }}
        />
      ),
    },
    {
      title: 'Proxy Key',
      dataIndex: 'proxy_api_key',
      width: 170,
      hideInSearch: true,
      render: (_, record) => (
        <Typography.Text copyable={{ text: record.proxy_api_key }}>
          {maskSecret(record.proxy_api_key)}
        </Typography.Text>
      ),
    },
    {
      title: '本地用量',
      dataIndex: ['usage', 'request_count'],
      width: 210,
      hideInSearch: true,
      render: (_, record) => (
        <Space direction="vertical" size={2}>
          <Typography.Text>
            {formatNumber(record.usage?.request_count)} requests
          </Typography.Text>
          <Typography.Text type="secondary">
            in {formatNumber(record.usage?.input_tokens)} / out{' '}
            {formatNumber(record.usage?.output_tokens)}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: 'Quota',
      dataIndex: 'quota',
      width: 180,
      hideInSearch: true,
      render: (_, record) => (
        <Space direction="vertical" size={2}>
          <Typography.Text>
            Primary{' '}
            {formatPercent(
              record.quota?.primary_rate_limit?.window?.used_percent,
            )}
          </Typography.Text>
          <Typography.Text type="secondary">
            {record.quota_fetched_at
              ? formatDateTime(record.quota_fetched_at)
              : '未刷新'}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 170,
      hideInSearch: true,
      render: (_, record) => formatDateTime(record.updated_at),
    },
    {
      title: '操作',
      valueType: 'option',
      width: 220,
      render: (_, record) => (
        <Space size={8}>
          <Tooltip title="编辑">
            <Button
              icon={<EditOutlined />}
              size="small"
              onClick={() => {
                setCurrent(record);
                setEditOpen(true);
              }}
            />
          </Tooltip>
          <Tooltip title="刷新 token">
            <Button
              icon={<ReloadOutlined />}
              size="small"
              disabled={record.provider === 'custom'}
              onClick={async () => {
                await refreshAccount(record.id);
                message.success('刷新完成');
                reload();
              }}
            />
          </Tooltip>
          <Tooltip title="重置状态">
            <Button
              icon={<ReloadOutlined />}
              size="small"
              disabled={record.status === 'active'}
              onClick={async () => {
                await handleResetStatus(record);
              }}
            />
          </Tooltip>
          <Popconfirm
            title="删除账号"
            description="删除后无法从前端恢复。"
            onConfirm={async () => {
              await deleteAccount(record.id);
              message.success('账号已删除');
              reload();
            }}
          >
            <Button danger icon={<DeleteOutlined />} size="small" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const pool = authReq.data?.pool;
  const providerData = useMemo(
    () => [
      { label: 'OpenAI', value: 'openai' },
      { label: 'Custom', value: 'custom' },
    ],
    [],
  );

  return (
    <PageContainer
      title="账号池"
      extra={[
        <Button
          key="oauth"
          icon={<ExportOutlined />}
          onClick={() => startOAuth()}
        >
          OpenAI OAuth
        </Button>,
        <Button
          key="import"
          icon={<KeyOutlined />}
          onClick={() => setImportOpen(true)}
        >
          导入凭证
        </Button>,
        <Button
          key="custom"
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setCustomOpen(true)}
        >
          自定义上游
        </Button>,
      ]}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <ProCard>
          <div className={styles.summaryGrid}>
            {poolLabels.map(([key, label]) => (
              <div className={styles.summaryItem} key={key}>
                <div className={styles.summaryLabel}>{label}</div>
                <div className={styles.summaryValue}>{pool?.[key] ?? 0}</div>
              </div>
            ))}
          </div>
        </ProCard>

        <ProTable<Prism.Account>
          actionRef={actionRef}
          rowKey="id"
          columns={columns}
          search={{ labelWidth: 88 }}
          request={async (params) => {
            const data = await listAccounts();
            const keyword = String(params.keyword || '').toLowerCase();
            const filtered = data.filter((item) => {
              const matchedKeyword =
                !keyword ||
                [
                  item.email,
                  item.label,
                  item.id,
                  item.account_id,
                  item.custom_base_url,
                ]
                  .filter(Boolean)
                  .some((value) =>
                    String(value).toLowerCase().includes(keyword),
                  );
              const matchedProvider =
                !params.provider || item.provider === params.provider;
              const matchedPlan =
                !params.plan_type || item.plan_type === params.plan_type;
              return matchedKeyword && matchedProvider && matchedPlan;
            });
            return {
              data: filtered,
              success: true,
              total: filtered.length,
            };
          }}
          toolbar={{
            search: {
              placeholder: '搜索邮箱、标签、ID',
              onSearch: (keyword) => {
                actionRef.current?.reload();
              },
            },
          }}
          options={{ reload: true, density: true, setting: true }}
          scroll={{ x: 1280 }}
        />
      </Space>

      <ModalForm
        title="创建自定义上游账号"
        open={customOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => setCustomOpen(false),
        }}
        onFinish={async (values) => {
          await createCustomAccount({
            ...(values as Prism.CustomAccountPayload),
            custom_endpoint_type: customEndpointValueFromForm(values),
          });
          message.success('自定义账号已创建');
          setCustomOpen(false);
          reload();
          return true;
        }}
      >
        <ProFormText name="label" label="展示名" placeholder="OpenRouter" />
        <ProFormText name="plan_type" label="套餐标识" initialValue="custom" />
        <ProFormText
          name="custom_base_url"
          label="Base URL"
          rules={[{ required: true, message: '请输入 Base URL' }]}
          placeholder="https://api.example.com"
        />
        <ProFormText.Password
          name="custom_api_key"
          label="Bearer Token"
          rules={[{ required: true, message: '请输入上游 API Key' }]}
        />
        <ProFormText
          name="custom_user_agent"
          label="User-Agent"
          placeholder="留空则不显式设置"
        />
        <ProFormSelect
          name="custom_endpoint_mode"
          label="Endpoint 类型"
          initialValue="responses"
          options={[
            {
              label: '/v1/chat/completions',
              value: 'chat_completions',
            },
            { label: '/v1/responses', value: 'responses' },
            { label: '自定义更多', value: 'custom' },
          ]}
        />
        <ProFormDependency name={['custom_endpoint_mode']}>
          {({ custom_endpoint_mode }) =>
            custom_endpoint_mode === 'custom' ? (
              <ProFormText
                name="custom_endpoint_custom_path"
                label="自定义聊天路径"
                rules={[
                  { required: true, message: '请输入自定义聊天路径或完整 URL' },
                ]}
                placeholder="/api/paas/v4/chat/completions 或完整 URL"
                extra="模型列表路径会自动推导，例如 /api/paas/v4/chat/completions -> /api/paas/v4/models"
              />
            ) : null
          }
        </ProFormDependency>
        <ProFormSwitch name="enabled" label="启用" initialValue />
      </ModalForm>

      <ModalForm
        title="编辑账号"
        open={editOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => {
            setEditOpen(false);
            setCurrent(undefined);
          },
        }}
        submitter={{
          render: (props, doms) => [
            current?.provider === 'openai' ? (
              <Button key="reauth" onClick={() => startOAuth(current)}>
                重新认证
              </Button>
            ) : null,
            ...doms,
          ],
        }}
        initialValues={customAccountFormValues(current)}
        onFinish={async (values) => {
          if (!current) return false;
          const payload = {
            ...(values as Prism.AccountUpdatePayload),
            custom_endpoint_type:
              values.provider === 'custom'
                ? customEndpointValueFromForm(values)
                : undefined,
          };
          await updateAccount(current.id, payload);
          message.success('账号已更新');
          setEditOpen(false);
          setCurrent(undefined);
          reload();
          return true;
        }}
      >
        {current?.provider === 'openai' ? (
          <Typography.Paragraph type="secondary" className={styles.formHint}>
            用于处理登录过期或 OpenAI
            侧登录被重置的情况。重新认证会更新当前账号的登录凭证，不会新建账号。
          </Typography.Paragraph>
        ) : null}
        <ProFormSelect
          name="provider"
          label="Provider"
          options={providerData}
        />
        <ProFormText name="label" label="展示名" />
        <ProFormText name="email" label="邮箱" />
        <ProFormText name="plan_type" label="套餐" />
        <ProFormText name="proxy_api_key" label="账号专属 Proxy Key" />
        <ProFormSwitch name="enabled" label="启用" />
        <ProFormDependency name={['provider']}>
          {({ provider }) =>
            provider === 'custom' ? (
              <>
                <ProFormText name="custom_base_url" label="自定义 Base URL" />
                <ProFormText.Password
                  name="custom_api_key"
                  label="自定义 API Key"
                  placeholder="留空表示不更新"
                />
                <ProFormText
                  name="custom_user_agent"
                  label="User-Agent"
                  placeholder="留空表示清空该账号的 User-Agent 配置"
                />
                <ProFormSelect
                  name="custom_endpoint_mode"
                  label="Endpoint 类型"
                  options={[
                    {
                      label: '/v1/chat/completions',
                      value: 'chat_completions',
                    },
                    { label: '/v1/responses', value: 'responses' },
                    { label: '自定义更多', value: 'custom' },
                  ]}
                />
                <ProFormDependency name={['custom_endpoint_mode']}>
                  {({ custom_endpoint_mode }) =>
                    custom_endpoint_mode === 'custom' ? (
                      <ProFormText
                        name="custom_endpoint_custom_path"
                        label="自定义聊天路径"
                        rules={[
                          {
                            required: true,
                            message: '请输入自定义聊天路径或完整 URL',
                          },
                        ]}
                        placeholder="/api/paas/v4/chat/completions 或完整 URL"
                        extra={`模型列表路径将自动推导为 ${customModelsEndpointPath(
                          current?.custom_endpoint_type,
                        )}`}
                      />
                    ) : null
                  }
                </ProFormDependency>
              </>
            ) : null
          }
        </ProFormDependency>
      </ModalForm>

      <ModalForm
        title="导入凭证"
        open={importOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => setImportOpen(false),
        }}
        submitter={{
          searchConfig: {
            submitText: importMode === 'cli' ? '导入 auth.json' : '导入 Token',
          },
        }}
        onFinish={async (values) => {
          if (importMode === 'cli') {
            await importCLI(values.auth_json);
          } else {
            await importToken(values.token, values.refresh_token);
          }
          message.success('导入成功');
          setImportOpen(false);
          reload();
          return true;
        }}
      >
        <Tabs
          activeKey={importMode}
          onChange={(key) => setImportMode(key as 'cli' | 'token')}
          items={[
            {
              key: 'cli',
              label: 'Codex CLI auth.json',
              children: (
                <ProFormTextArea
                  name="auth_json"
                  label="auth.json 内容"
                  fieldProps={{ rows: 8 }}
                  rules={
                    importMode === 'cli'
                      ? [{ required: true, message: '请输入 auth.json 内容' }]
                      : []
                  }
                />
              ),
            },
            {
              key: 'token',
              label: 'Access / Refresh Token',
              children: (
                <>
                  <ProFormText.Password
                    name="token"
                    label="Access Token"
                    rules={
                      importMode === 'token'
                        ? [{ required: true, message: '请输入 access token' }]
                        : []
                    }
                  />
                  <ProFormText.Password
                    name="refresh_token"
                    label="Refresh Token"
                    rules={
                      importMode === 'token'
                        ? [{ required: true, message: '请输入 refresh token' }]
                        : []
                    }
                  />
                </>
              ),
            },
          ]}
        />
      </ModalForm>

      <ModalForm
        title={
          oauthTargetAccount ? '完成 OpenAI 重新认证' : '完成 OpenAI OAuth'
        }
        open={oauthOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => {
            setOauthOpen(false);
            setOauthSession(undefined);
            setOauthTargetAccount(undefined);
          },
        }}
        onFinish={async (values) => {
          await codeRelay(values.callbackUrl);
          message.success(
            oauthTargetAccount ? '重新认证已完成' : 'OAuth 已完成',
          );
          setOauthOpen(false);
          setOauthSession(undefined);
          setOauthTargetAccount(undefined);
          setEditOpen(false);
          setCurrent(undefined);
          reload();
          return true;
        }}
      >
        <Descriptions column={1} size="small" style={{ marginBottom: 16 }}>
          {oauthTargetAccount ? (
            <Descriptions.Item label="目标账号">
              <AccountLabel account={oauthTargetAccount} />
            </Descriptions.Item>
          ) : null}
          <Descriptions.Item label="State">
            {oauthSession?.state || '-'}
          </Descriptions.Item>
          <Descriptions.Item label="回调端口">
            {oauthSession?.port || '-'}
          </Descriptions.Item>
        </Descriptions>
        <ProFormTextArea
          name="callbackUrl"
          label="回调 URL / code state 文本"
          fieldProps={{ rows: 5 }}
          rules={[{ required: true, message: '请输入回调信息' }]}
          placeholder="http://localhost:1455/auth/callback?code=...&state=..."
        />
      </ModalForm>

      <Drawer
        title="账号详情"
        width={720}
        open={!!current && !editOpen}
        onClose={() => {
          setCurrent(undefined);
        }}
      >
        {current ? (
          <>
            <Space style={{ marginBottom: 16 }}>
              <Button
                icon={<ReloadOutlined />}
                disabled={current.status === 'active'}
                onClick={() => handleResetStatus(current)}
              >
                重置状态
              </Button>
            </Space>
            <ProDescriptions<Prism.Account>
              column={2}
              dataSource={current}
              columns={[
                {
                  title: '账号',
                  render: () => <AccountLabel account={current} />,
                },
                {
                  title: 'Provider',
                  render: () => renderProviderTag(current.provider),
                },
                {
                  title: '状态',
                  render: () => renderStatusTag(current.status),
                },
                {
                  title: '启用',
                  render: () => (current.enabled ? '是' : '否'),
                },
                { title: 'ID', dataIndex: 'id', copyable: true },
                { title: '账号 ID', dataIndex: 'account_id', copyable: true },
                { title: '用户 ID', dataIndex: 'user_id', copyable: true },
                {
                  title: 'Proxy Key',
                  render: () => maskSecret(current.proxy_api_key),
                },
                {
                  title: 'Base URL',
                  dataIndex: 'custom_base_url',
                  copyable: true,
                },
                {
                  title: 'Chat Endpoint',
                  render: () => current.custom_endpoint_type || '-',
                },
                {
                  title: 'User-Agent',
                  render: () => current.custom_user_agent || '-',
                },
                {
                  title: 'Models Endpoint',
                  render: () =>
                    current.custom_endpoint_type
                      ? customModelsEndpointPath(current.custom_endpoint_type)
                      : '-',
                },
                {
                  title: '请求数',
                  render: () => formatNumber(current.usage?.request_count),
                },
                {
                  title: '输入 Token',
                  render: () => formatNumber(current.usage?.input_tokens),
                },
                {
                  title: '输出 Token',
                  render: () => formatNumber(current.usage?.output_tokens),
                },
                {
                  title: '空响应数',
                  render: () =>
                    formatNumber(current.usage?.empty_response_count),
                },
                {
                  title: '创建时间',
                  render: () => formatDateTime(current.created_at),
                },
                {
                  title: '更新时间',
                  render: () => formatDateTime(current.updated_at),
                },
                {
                  title: '过期时间',
                  render: () => formatDateTime(current.expires_at),
                },
                {
                  title: '最近刷新',
                  render: () => formatDateTime(current.last_refresh_at),
                },
                {
                  title: '限流恢复',
                  render: () => formatDateTime(current.rate_limited_until),
                },
                {
                  title: '官方额度更新时间',
                  render: () => formatDateTime(current.quota_fetched_at),
                },
                {
                  title: '5h 额度',
                  span: 2,
                  render: () =>
                    current.enabled
                      ? renderQuotaWindow(
                          current.quota?.primary_rate_limit,
                          '5h 额度',
                        )
                      : renderDisabledQuotaAlert(),
                },
                {
                  title: '7天额度',
                  span: 2,
                  render: () =>
                    current.enabled
                      ? renderQuotaWindow(
                          current.quota?.secondary_rate_limit,
                          '7天额度',
                        )
                      : renderDisabledQuotaAlert(),
                },
              ]}
            />
          </>
        ) : null}
      </Drawer>
    </PageContainer>
  );
};

export default AccountsPage;
