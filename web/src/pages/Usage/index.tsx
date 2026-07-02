import { useRawRequest } from '@/hooks/useRawRequest';
import {
  getModelCatalog,
  getRequestEvents,
  getRequestEventSummary,
  getUsageByAccountModels,
  getUsageByAccounts,
  getUsageByModels,
  getUsageHistory,
  getUsageSummary,
  listAccounts,
} from '@/services/api';
import {
  displayAccount,
  formatCompactNumber,
  formatDateTime,
  formatNumber,
  maskUsageAccountDisplay,
  toRFC3339,
} from '@/utils/prism';
import { Column, Line, Pie } from '@ant-design/charts';
import { ReloadOutlined } from '@ant-design/icons';
import type { ActionType, ProColumns } from '@ant-design/pro-components';
import {
  PageContainer,
  ProCard,
  ProTable,
  StatisticCard,
} from '@ant-design/pro-components';
import {
  Button,
  Col,
  DatePicker,
  Empty,
  Row,
  Segmented,
  Space,
  Tabs,
  Tag,
} from 'antd';
import dayjs from 'dayjs';
import React, { useMemo, useRef, useState } from 'react';
import styles from './index.less';

const { RangePicker } = DatePicker;

const UsagePage: React.FC = () => {
  const [granularity, setGranularity] = useState<Prism.Granularity>('day');
  const [historyRange, setHistoryRange] = useState<[string, string]>(() => {
    const now = new Date();
    const from = new Date(now);
    from.setDate(from.getDate() - 29);
    return [from.toISOString(), now.toISOString()];
  });
  const actionRef = useRef<ActionType>();

  const summaryReq = useRawRequest(getUsageSummary);
  const historyReq = useRawRequest(
    () =>
      getUsageHistory({
        granularity,
        from: historyRange[0],
        to: historyRange[1],
      }),
    {
      refreshDeps: [granularity, historyRange[0], historyRange[1]],
    },
  );
  const accountReq = useRawRequest(getUsageByAccounts);
  const modelReq = useRawRequest(getUsageByModels);
  const accountModelReq = useRawRequest(getUsageByAccountModels);
  const requestSummaryReq = useRawRequest(getRequestEventSummary);
  const accountsReq = useRawRequest(listAccounts);
  const catalogReq = useRawRequest(getModelCatalog);

  const refreshAll = () => {
    summaryReq.refresh();
    historyReq.refresh();
    accountReq.refresh();
    modelReq.refresh();
    accountModelReq.refresh();
    requestSummaryReq.refresh();
    actionRef.current?.reload();
  };

  const totalTokens =
    (summaryReq.data?.total_input_tokens || 0) +
    (summaryReq.data?.total_output_tokens || 0) +
    (summaryReq.data?.total_cached_tokens || 0);

  const lineData =
    historyReq.data?.points?.flatMap((point) => [
      {
        timestamp: formatDateTime(point.timestamp),
        type: '输入 Token',
        value: point.input_tokens,
      },
      {
        timestamp: formatDateTime(point.timestamp),
        type: '输出 Token',
        value: point.output_tokens,
      },
      {
        timestamp: formatDateTime(point.timestamp),
        type: '缓存命中 Token',
        value: point.cached_tokens,
      },
    ]) || [];

  const pieData = [
    { type: '输入 Token', value: summaryReq.data?.total_input_tokens || 0 },
    { type: '输出 Token', value: summaryReq.data?.total_output_tokens || 0 },
    {
      type: '缓存命中 Token',
      value: summaryReq.data?.total_cached_tokens || 0,
    },
  ].filter((item) => item.value > 0);

  const accountOptions = useMemo(
    () =>
      accountsReq.data?.map((account) => ({
        label: maskUsageAccountDisplay({
          account_provider: account.provider,
          account_display_name: displayAccount(account),
          account_label: account.label,
          account_email: account.email,
        }),
        value: account.id,
      })) || [],
    [accountsReq.data],
  );

  const modelOptions = useMemo(
    () =>
      catalogReq.data?.map((model) => ({
        label: model.id,
        value: model.id,
      })) || [],
    [catalogReq.data],
  );

  const usageAccountLabel = (item?: {
    account_provider?: string;
    account_display_name?: string;
    account_label?: string;
    account_email?: string;
  }) => maskUsageAccountDisplay(item);

  const requestSuccessRate = useMemo(() => {
    const total = requestSummaryReq.data?.total_request_count || 0;
    const success = requestSummaryReq.data?.success_request_count || 0;
    if (!total) return '-';
    return `${((success / total) * 100).toFixed(1)}%`;
  }, [requestSummaryReq.data]);

  const summaryCards = [
    {
      key: 'usage-request-count',
      loading: summaryReq.loading,
      statistic: {
        title: '累计请求',
        value: summaryReq.data?.total_request_count || 0,
        formatter: (value: React.ReactNode) => formatNumber(Number(value)),
      },
    },
    {
      key: 'usage-input-tokens',
      loading: summaryReq.loading,
      statistic: {
        title: '输入 Token',
        value: summaryReq.data?.total_input_tokens || 0,
        formatter: (value: React.ReactNode) =>
          formatCompactNumber(Number(value)),
      },
    },
    {
      key: 'usage-output-tokens',
      loading: summaryReq.loading,
      statistic: {
        title: '输出 Token',
        value: summaryReq.data?.total_output_tokens || 0,
        formatter: (value: React.ReactNode) =>
          formatCompactNumber(Number(value)),
      },
    },
    {
      key: 'usage-cached-tokens',
      loading: summaryReq.loading,
      statistic: {
        title: '缓存命中 Token',
        value: summaryReq.data?.total_cached_tokens || 0,
        formatter: (value: React.ReactNode) =>
          formatCompactNumber(Number(value)),
      },
    },
    {
      key: 'request-attempt-count',
      loading: requestSummaryReq.loading,
      statistic: {
        title: '请求尝试数',
        value: requestSummaryReq.data?.total_request_count || 0,
        formatter: (value: React.ReactNode) => formatNumber(Number(value)),
      },
    },
    {
      key: 'request-avg-duration',
      loading: requestSummaryReq.loading,
      statistic: {
        title: '平均总耗时',
        value: requestSummaryReq.data?.avg_duration_ms || 0,
        suffix: 'ms',
        formatter: (value: React.ReactNode) =>
          formatNumber(Math.round(Number(value))),
      },
    },
    {
      key: 'request-avg-first-token',
      loading: requestSummaryReq.loading,
      statistic: {
        title: '平均首 Token',
        value: requestSummaryReq.data?.avg_first_token_ms || 0,
        suffix: 'ms',
        formatter: (value: React.ReactNode) =>
          requestSummaryReq.data?.avg_first_token_ms !== undefined
            ? formatNumber(Math.round(Number(value)))
            : '-',
      },
    },
    {
      key: 'usage-total-tokens',
      loading: summaryReq.loading,
      statistic: {
        title: '总 Token',
        value: totalTokens,
        formatter: (value: React.ReactNode) =>
          formatCompactNumber(Number(value)),
      },
    },
    {
      key: 'request-success-rate',
      loading: requestSummaryReq.loading,
      statistic: {
        title: '请求成功率',
        value: requestSuccessRate,
      },
    },
    {
      key: 'request-failed-count',
      loading: requestSummaryReq.loading,
      statistic: {
        title: '失败尝试',
        value: requestSummaryReq.data?.failed_request_count || 0,
        formatter: (value: React.ReactNode) => formatNumber(Number(value)),
      },
    },
  ];

  const requestEventColumns: ProColumns<Prism.RequestEvent>[] = [
    {
      title: '完成时间',
      dataIndex: 'completed_at',
      valueType: 'dateTimeRange',
      render: (_, record) => formatDateTime(record.completed_at),
      width: 180,
    },
    {
      title: '入口',
      dataIndex: 'source_path',
      valueType: 'select',
      fieldProps: {
        options: [
          { label: '/v1/responses', value: '/v1/responses' },
          { label: '/v1/chat/completions', value: '/v1/chat/completions' },
        ],
      },
      render: (_, record) => <Tag>{record.source_path}</Tag>,
      width: 170,
    },
    {
      title: '账号',
      dataIndex: 'account',
      valueType: 'select',
      fieldProps: { options: accountOptions, showSearch: true },
      render: (_, record) => usageAccountLabel(record),
      width: 220,
    },
    {
      title: '请求模型',
      dataIndex: 'model',
      valueType: 'select',
      fieldProps: { options: modelOptions, showSearch: true },
      render: (_, record) => <Tag>{record.requested_model}</Tag>,
      width: 160,
    },
    {
      title: '路由模型',
      dataIndex: 'routed_model',
      hideInSearch: true,
      render: (_, record) => <Tag color="blue">{record.routed_model}</Tag>,
      width: 160,
    },
    {
      title: '总耗时',
      dataIndex: 'duration_ms',
      hideInSearch: true,
      render: (_, record) => `${formatNumber(record.duration_ms)} ms`,
      width: 130,
      sorter: (a, b) => a.duration_ms - b.duration_ms,
    },
    {
      title: '首 Token',
      dataIndex: 'first_token_ms',
      hideInSearch: true,
      render: (_, record) =>
        record.first_token_ms !== undefined
          ? `${formatNumber(record.first_token_ms)} ms`
          : '-',
      width: 130,
      sorter: (a, b) =>
        (a.first_token_ms || Number.MAX_SAFE_INTEGER) -
        (b.first_token_ms || Number.MAX_SAFE_INTEGER),
    },
    {
      title: '状态',
      dataIndex: 'success',
      valueType: 'select',
      fieldProps: {
        options: [
          { label: '成功', value: 'true' },
          { label: '失败', value: 'false' },
        ],
      },
      render: (_, record) =>
        record.success ? (
          <Tag color="success">成功</Tag>
        ) : (
          <Tag color="error">失败</Tag>
        ),
      width: 100,
    },
    {
      title: '重试序号',
      dataIndex: 'retry_attempt',
      hideInSearch: true,
      width: 100,
      render: (_, record) => formatNumber(record.retry_attempt),
    },
    {
      title: '状态码',
      dataIndex: 'status_code',
      hideInSearch: true,
      width: 100,
      render: (_, record) =>
        record.status_code !== undefined ? record.status_code : '-',
    },
  ];

  const eventColumns: ProColumns<Prism.RequestEvent>[] = [
    {
      title: '请求时间',
      dataIndex: 'completed_at',
      valueType: 'dateTimeRange',
      render: (_, record) => formatDateTime(record.completed_at),
      width: 180,
    },
    {
      title: '入口',
      dataIndex: 'source_path',
      valueType: 'select',
      fieldProps: {
        options: [
          { label: '/v1/responses', value: '/v1/responses' },
          { label: '/v1/chat/completions', value: '/v1/chat/completions' },
        ],
      },
      render: (_, record) => <Tag>{record.source_path}</Tag>,
      width: 170,
    },
    {
      title: '账号',
      dataIndex: 'account',
      valueType: 'select',
      fieldProps: { options: accountOptions, showSearch: true },
      render: (_, record) => usageAccountLabel(record),
      width: 220,
    },
    {
      title: '请求模型',
      dataIndex: 'model',
      valueType: 'select',
      fieldProps: { options: modelOptions, showSearch: true },
      render: (_, record) => <Tag>{record.requested_model}</Tag>,
      width: 160,
    },
    {
      title: '路由模型',
      dataIndex: 'routed_model',
      hideInSearch: true,
      render: (_, record) => <Tag color="blue">{record.routed_model}</Tag>,
      width: 160,
    },
    {
      title: '总耗时',
      dataIndex: 'duration_ms',
      hideInSearch: true,
      render: (_, record) => `${formatNumber(record.duration_ms)} ms`,
      width: 130,
    },
    {
      title: '首 Token',
      dataIndex: 'first_token_ms',
      hideInSearch: true,
      render: (_, record) =>
        record.first_token_ms !== undefined
          ? `${formatNumber(record.first_token_ms)} ms`
          : '-',
      width: 130,
    },
    {
      title: '输入 Token',
      dataIndex: 'input_tokens',
      hideInSearch: true,
      width: 130,
      render: (_, record) =>
        record.input_tokens !== undefined
          ? formatNumber(record.input_tokens)
          : '-',
    },
    {
      title: '输出 Token',
      dataIndex: 'output_tokens',
      hideInSearch: true,
      width: 130,
      render: (_, record) =>
        record.output_tokens !== undefined
          ? formatNumber(record.output_tokens)
          : '-',
    },
    {
      title: '缓存命中',
      dataIndex: 'cached_tokens',
      hideInSearch: true,
      width: 130,
      render: (_, record) =>
        record.cached_tokens !== undefined
          ? formatNumber(record.cached_tokens)
          : '-',
    },
    {
      title: '推理 Token',
      dataIndex: 'reasoning_tokens',
      hideInSearch: true,
      width: 130,
      render: (_, record) =>
        record.reasoning_tokens !== undefined
          ? formatNumber(record.reasoning_tokens)
          : '-',
    },
    {
      title: '状态',
      dataIndex: 'success',
      valueType: 'select',
      fieldProps: {
        options: [
          { label: '成功', value: 'true' },
          { label: '失败', value: 'false' },
        ],
      },
      render: (_, record) =>
        record.success ? (
          <Tag color="success">成功</Tag>
        ) : (
          <Tag color="error">失败</Tag>
        ),
      width: 100,
    },
    {
      title: '重试序号',
      dataIndex: 'retry_attempt',
      hideInSearch: true,
      width: 100,
      render: (_, record) => formatNumber(record.retry_attempt),
    },
    {
      title: '状态码',
      dataIndex: 'status_code',
      hideInSearch: true,
      width: 100,
      render: (_, record) =>
        record.status_code !== undefined ? record.status_code : '-',
    },
  ];

  const accountColumns: ProColumns<Prism.AccountUsageBreakdown>[] = [
    {
      title: '账号',
      dataIndex: 'account_email',
      render: (_, record) => usageAccountLabel(record),
    },
    {
      title: '请求数',
      dataIndex: 'total_request_count',
      sorter: (a, b) => a.total_request_count - b.total_request_count,
      render: (_, record) => formatNumber(record.total_request_count),
    },
    {
      title: '输入 Token',
      dataIndex: 'total_input_tokens',
      sorter: (a, b) => a.total_input_tokens - b.total_input_tokens,
      render: (_, record) => formatNumber(record.total_input_tokens),
    },
    {
      title: '输出 Token',
      dataIndex: 'total_output_tokens',
      sorter: (a, b) => a.total_output_tokens - b.total_output_tokens,
      render: (_, record) => formatNumber(record.total_output_tokens),
    },
    {
      title: '缓存命中',
      dataIndex: 'total_cached_tokens',
      sorter: (a, b) => a.total_cached_tokens - b.total_cached_tokens,
      render: (_, record) => formatNumber(record.total_cached_tokens),
    },
    {
      title: '最近记录',
      dataIndex: 'last_recorded_at',
      render: (_, record) => formatDateTime(record.last_recorded_at),
    },
  ];

  const modelColumns: ProColumns<Prism.ModelUsageBreakdown>[] = [
    {
      title: '模型',
      dataIndex: 'model_id',
      render: (_, record) => <Tag>{record.model_id}</Tag>,
    },
    {
      title: '请求数',
      dataIndex: 'total_request_count',
      sorter: (a, b) => a.total_request_count - b.total_request_count,
      render: (_, record) => formatNumber(record.total_request_count),
    },
    {
      title: '输入 Token',
      dataIndex: 'total_input_tokens',
      sorter: (a, b) => a.total_input_tokens - b.total_input_tokens,
      render: (_, record) => formatNumber(record.total_input_tokens),
    },
    {
      title: '输出 Token',
      dataIndex: 'total_output_tokens',
      sorter: (a, b) => a.total_output_tokens - b.total_output_tokens,
      render: (_, record) => formatNumber(record.total_output_tokens),
    },
    {
      title: '缓存命中',
      dataIndex: 'total_cached_tokens',
      sorter: (a, b) => a.total_cached_tokens - b.total_cached_tokens,
      render: (_, record) => formatNumber(record.total_cached_tokens),
    },
    {
      title: '最近记录',
      dataIndex: 'last_recorded_at',
      render: (_, record) => formatDateTime(record.last_recorded_at),
    },
  ];

  const accountModelColumns: ProColumns<Prism.AccountModelUsageBreakdown>[] = [
    {
      title: '账号',
      dataIndex: 'account_email',
      render: (_, record) => usageAccountLabel(record),
    },
    {
      title: '模型',
      dataIndex: 'model_id',
      render: (_, record) => <Tag>{record.model_id}</Tag>,
    },
    {
      title: '请求数',
      dataIndex: 'total_request_count',
      sorter: (a, b) => a.total_request_count - b.total_request_count,
      render: (_, record) => formatNumber(record.total_request_count),
    },
    {
      title: '总 Token',
      render: (_, record) =>
        formatNumber(
          record.total_input_tokens +
            record.total_output_tokens +
            record.total_cached_tokens,
        ),
      sorter: (a, b) =>
        a.total_input_tokens +
        a.total_output_tokens +
        a.total_cached_tokens -
        (b.total_input_tokens + b.total_output_tokens + b.total_cached_tokens),
    },
    {
      title: '缓存命中',
      dataIndex: 'total_cached_tokens',
      sorter: (a, b) => a.total_cached_tokens - b.total_cached_tokens,
      render: (_, record) => formatNumber(record.total_cached_tokens),
    },
    {
      title: '最近记录',
      dataIndex: 'last_recorded_at',
      render: (_, record) => formatDateTime(record.last_recorded_at),
    },
  ];

  return (
    <PageContainer
      title="用量统计"
      extra={[
        <Button key="refresh" icon={<ReloadOutlined />} onClick={refreshAll}>
          刷新
        </Button>,
      ]}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <div className={styles.summaryGrid}>
          {summaryCards.map((card) => (
            <StatisticCard
              key={card.key}
              className={styles.summaryCard}
              loading={card.loading}
              statistic={card.statistic}
            />
          ))}
        </div>

        <Row gutter={[16, 16]}>
          <Col xs={24} xl={16}>
            <ProCard
              title="Token 趋势"
              loading={historyReq.loading}
              extra={
                <Space wrap className={styles.chartToolbar}>
                  <RangePicker
                    allowClear={false}
                    value={[dayjs(historyRange[0]), dayjs(historyRange[1])]}
                    onChange={(values) => {
                      if (!values?.[0] || !values?.[1]) return;
                      setHistoryRange([
                        values[0].toISOString(),
                        values[1].toISOString(),
                      ]);
                    }}
                  />
                  <Segmented
                    value={granularity}
                    options={[
                      { label: '小时', value: 'hour' },
                      { label: '天', value: 'day' },
                      { label: '月', value: 'month' },
                    ]}
                    onChange={(value) =>
                      setGranularity(value as Prism.Granularity)
                    }
                  />
                </Space>
              }
            >
              {lineData.length ? (
                <Line
                  theme="classicDark"
                  height={320}
                  data={lineData}
                  xField="timestamp"
                  yField="value"
                  colorField="type"
                  axis={{ y: { labelFormatter: '~s' } }}
                  interaction={{ tooltip: { shared: true } }}
                />
              ) : (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
              )}
            </ProCard>
          </Col>
          <Col xs={24} xl={8}>
            <ProCard title="Token 构成" loading={summaryReq.loading}>
              {pieData.length ? (
                <Pie
                  theme="classicDark"
                  height={320}
                  data={pieData}
                  angleField="value"
                  colorField="type"
                  innerRadius={0.62}
                  label={{ text: 'value' }}
                  legend={{ color: { position: 'bottom' } }}
                />
              ) : (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
              )}
            </ProCard>
          </Col>
        </Row>

        <ProCard title="聚合分析">
          <Tabs
            items={[
              {
                key: 'accounts',
                label: '按账号',
                children: (
                  <>
                    <Column
                      theme="classicDark"
                      height={260}
                      data={(accountReq.data || []).map((item) => ({
                        account: usageAccountLabel(item),
                        value:
                          item.total_input_tokens +
                          item.total_output_tokens +
                          item.total_cached_tokens,
                      }))}
                      xField="account"
                      yField="value"
                      axis={{ y: { labelFormatter: '~s' } }}
                    />
                    <ProTable<Prism.AccountUsageBreakdown>
                      rowKey="account_identity"
                      search={false}
                      options={false}
                      dataSource={accountReq.data || []}
                      columns={accountColumns}
                      pagination={{ pageSize: 8 }}
                    />
                  </>
                ),
              },
              {
                key: 'models',
                label: '按模型',
                children: (
                  <ProTable<Prism.ModelUsageBreakdown>
                    rowKey="model_id"
                    search={false}
                    options={false}
                    dataSource={modelReq.data || []}
                    columns={modelColumns}
                    pagination={{ pageSize: 8 }}
                  />
                ),
              },
              {
                key: 'account-models',
                label: '账号 + 模型',
                children: (
                  <ProTable<Prism.AccountModelUsageBreakdown>
                    rowKey={(record) =>
                      `${record.account_identity}-${record.model_id}`
                    }
                    search={false}
                    options={false}
                    dataSource={accountModelReq.data || []}
                    columns={accountModelColumns}
                    pagination={{ pageSize: 8 }}
                  />
                ),
              },
            ]}
          />
        </ProCard>

        <ProTable<Prism.RequestEvent>
          actionRef={actionRef}
          rowKey="id"
          headerTitle="用量事件明细"
          columns={eventColumns}
          search={{ labelWidth: 88 }}
          request={async (params) => {
            const range = params.completed_at as unknown as
              | string[]
              | undefined;
            const result = await getRequestEvents({
              page: params.current,
              page_size: params.pageSize,
              from: toRFC3339(range?.[0]),
              to: toRFC3339(range?.[1]),
              model: params.model as string | undefined,
              account: params.account as string | undefined,
              source_path: params.source_path as string | undefined,
              success:
                params.success === 'true'
                  ? true
                  : params.success === 'false'
                  ? false
                  : undefined,
            });
            return {
              data: result.items,
              success: true,
              total: result.total,
            };
          }}
          pagination={{ pageSize: 20 }}
          scroll={{ x: 1480 }}
        />
      </Space>
    </PageContainer>
  );
};

export default UsagePage;
