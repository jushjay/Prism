import { useRawRequest } from '@/hooks/useRawRequest';
import {
  getAuthStatus,
  getModelCatalog,
  getOverview,
  getSettings,
  getUsageHistory,
  health,
} from '@/services/api';
import {
  formatCompactNumber,
  formatDateTime,
  formatNumber,
  maskSecret,
} from '@/utils/prism';
import { Line, Pie } from '@ant-design/charts';
import {
  ApiOutlined,
  CheckCircleOutlined,
  ClockCircleOutlined,
  DatabaseOutlined,
  KeyOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import {
  PageContainer,
  ProCard,
  ProDescriptions,
  StatisticCard,
} from '@ant-design/pro-components';
import { Alert, Button, Col, Empty, Row, Space, Tag, Typography } from 'antd';
import React from 'react';
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

const OverviewPage: React.FC = () => {
  const overviewReq = useRawRequest(getOverview);
  const healthReq = useRawRequest(health);
  const authReq = useRawRequest(getAuthStatus);
  const settingsReq = useRawRequest(getSettings);
  const catalogReq = useRawRequest(getModelCatalog);
  const historyReq = useRawRequest(() =>
    getUsageHistory({ granularity: 'day' }),
  );

  const refreshAll = () => {
    overviewReq.refresh();
    healthReq.refresh();
    authReq.refresh();
    settingsReq.refresh();
    catalogReq.refresh();
    historyReq.refresh();
  };

  const overview = overviewReq.data;
  const usage = overview?.usage;
  const pool = overview?.pool || authReq.data?.pool;
  const totalTokens =
    (usage?.total_input_tokens || 0) + (usage?.total_output_tokens || 0);

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
        type: '请求数',
        value: point.request_count,
      },
    ]) || [];

  const poolPieData = pool
    ? poolLabels
        .filter(([key]) => key !== 'total')
        .map(([key, label]) => ({
          type: label,
          value: pool[key] || 0,
        }))
        .filter((item) => item.value > 0)
    : [];

  return (
    <PageContainer
      title="运维概览"
      extra={[
        <Button key="refresh" icon={<ReloadOutlined />} onClick={refreshAll}>
          刷新
        </Button>,
      ]}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Row gutter={[16, 16]}>
          <Col xs={24} sm={12} xl={6}>
            <StatisticCard
              loading={overviewReq.loading}
              statistic={{
                title: '累计请求',
                value: usage?.total_request_count || 0,
                prefix: <ApiOutlined />,
                formatter: (value) => formatNumber(Number(value)),
              }}
            />
          </Col>
          <Col xs={24} sm={12} xl={6}>
            <StatisticCard
              loading={overviewReq.loading}
              statistic={{
                title: '累计 Token',
                value: totalTokens,
                prefix: <DatabaseOutlined />,
                formatter: (value) => formatCompactNumber(Number(value)),
              }}
            />
          </Col>
          <Col xs={24} sm={12} xl={6}>
            <StatisticCard
              loading={overviewReq.loading}
              statistic={{
                title: '模型总数',
                value: overview?.models_total || catalogReq.data?.length || 0,
                prefix: <CheckCircleOutlined />,
              }}
            />
          </Col>
          <Col xs={24} sm={12} xl={6}>
            <StatisticCard
              loading={overviewReq.loading}
              statistic={{
                title: '活跃账号',
                value: pool?.active || 0,
                prefix: <KeyOutlined />,
                suffix: `/ ${pool?.total || 0}`,
              }}
            />
          </Col>
        </Row>

        <Row gutter={[16, 16]}>
          <Col xs={24} xl={15}>
            <ProCard
              title="用量趋势"
              loading={historyReq.loading}
              extra={<Tag>按日聚合</Tag>}
            >
              {lineData.length ? (
                <Line
                  height={320}
                  data={lineData}
                  xField="timestamp"
                  yField="value"
                  colorField="type"
                  axis={{
                    y: { labelFormatter: '~s' },
                  }}
                  interaction={{ tooltip: { shared: true } }}
                />
              ) : (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
              )}
            </ProCard>
          </Col>
          <Col xs={24} xl={9}>
            <ProCard title="账号池状态" loading={overviewReq.loading}>
              <div className={styles.poolGrid}>
                {poolLabels.map(([key, label]) => (
                  <div className={styles.poolItem} key={key}>
                    <div className={styles.poolLabel}>{label}</div>
                    <div className={styles.poolValue}>{pool?.[key] ?? 0}</div>
                  </div>
                ))}
              </div>
              {poolPieData.length ? (
                <Pie
                  height={220}
                  data={poolPieData}
                  angleField="value"
                  colorField="type"
                  innerRadius={0.62}
                  label={{ text: 'value', position: 'outside' }}
                  legend={{ color: { position: 'bottom' } }}
                />
              ) : null}
            </ProCard>
          </Col>
        </Row>

        <Row gutter={[16, 16]}>
          <Col xs={24} lg={12}>
            <ProCard title="运行状态">
              <ProDescriptions column={1} size="small">
                <ProDescriptions.Item label="健康检查">
                  <Tag
                    color={
                      healthReq.data?.ok || healthReq.data?.status
                        ? 'green'
                        : 'default'
                    }
                  >
                    {healthReq.data?.service ||
                      healthReq.data?.status ||
                      '未知'}
                  </Tag>
                </ProDescriptions.Item>
                <ProDescriptions.Item label="默认模型">
                  {overview?.default_model ||
                    settingsReq.data?.default_model ||
                    '-'}
                </ProDescriptions.Item>
                <ProDescriptions.Item label="默认 reasoning">
                  {settingsReq.data?.default_reasoning_effort || '-'}
                </ProDescriptions.Item>
                <ProDescriptions.Item label="OpenAI Base URL">
                  {settingsReq.data?.openai_base_url || '-'}
                </ProDescriptions.Item>
                <ProDescriptions.Item label="客户端版本">
                  {settingsReq.data?.openai_client_version || '-'}
                </ProDescriptions.Item>
                <ProDescriptions.Item label="最后用量记录">
                  <ClockCircleOutlined />{' '}
                  {formatDateTime(usage?.last_recorded_at)}
                </ProDescriptions.Item>
              </ProDescriptions>
            </ProCard>
          </Col>
          <Col xs={24} lg={12}>
            <ProCard title="接入参数">
              <Alert
                showIcon
                type="info"
                message="OpenAI 兼容接口使用 Bearer Token，后台页面使用 Dashboard Session Cookie。"
                style={{ marginBottom: 12 }}
              />
              <ProDescriptions column={1} size="small">
                <ProDescriptions.Item label="全局 Bearer Token">
                  <Typography.Text copyable>
                    {maskSecret(authReq.data?.proxy_api_key)}
                  </Typography.Text>
                </ProDescriptions.Item>
                <ProDescriptions.Item label="Responses Endpoint">
                  <Typography.Text copyable>/v1/responses</Typography.Text>
                </ProDescriptions.Item>
                <ProDescriptions.Item label="Chat Completions Endpoint">
                  <Typography.Text copyable>
                    /v1/chat/completions
                  </Typography.Text>
                </ProDescriptions.Item>
              </ProDescriptions>
              <pre className={styles.snippet}>
                {`curl http://localhost:8080/v1/models \\
  -H "Authorization: Bearer ${
    authReq.data?.proxy_api_key || '<PROXY_API_KEY>'
  }"`}
              </pre>
            </ProCard>
          </Col>
        </Row>
      </Space>
    </PageContainer>
  );
};

export default OverviewPage;
