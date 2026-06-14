import { useRawRequest } from '@/hooks/useRawRequest';
import { dashboardLogout, getSettings } from '@/services/api';
import { formatDateTime, maskSecret } from '@/utils/prism';
import { LogoutOutlined, ReloadOutlined } from '@ant-design/icons';
import {
  PageContainer,
  ProCard,
  ProDescriptions,
} from '@ant-design/pro-components';
import { history, useModel } from '@umijs/max';
import { Button, Space, Tag, Typography, message } from 'antd';
import React from 'react';

const SettingsPage: React.FC = () => {
  const settingsReq = useRawRequest(getSettings);
  const { initialState, setInitialState } = useModel('@@initialState');

  const settings = settingsReq.data;

  return (
    <PageContainer
      title="运行参数"
      extra={[
        <Button
          key="reload"
          icon={<ReloadOutlined />}
          onClick={settingsReq.refresh}
        >
          刷新
        </Button>,
        <Button
          key="logout"
          icon={<LogoutOutlined />}
          onClick={async () => {
            await dashboardLogout();
            setInitialState({
              authenticated: false,
              authStatus: initialState?.authStatus,
            });
            message.success('已退出后台会话');
            history.push('/login');
          }}
        >
          退出登录
        </Button>,
      ]}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <ProCard title="服务与模型">
          <ProDescriptions<Prism.Settings>
            loading={settingsReq.loading}
            dataSource={settings}
            column={2}
            columns={[
              {
                title: '全局 Proxy Key',
                render: (_, record) => (
                  <Typography.Text copyable={{ text: record?.proxy_api_key }}>
                    {maskSecret(record?.proxy_api_key)}
                  </Typography.Text>
                ),
              },
              { title: '默认模型', dataIndex: 'default_model' },
              {
                title: '默认 Reasoning',
                dataIndex: 'default_reasoning_effort',
              },
              {
                title: '注入 Desktop Context',
                render: (_, record) =>
                  record?.inject_desktop_context ? (
                    <Tag color="green">开启</Tag>
                  ) : (
                    <Tag>关闭</Tag>
                  ),
              },
              {
                title: 'OpenAI Base URL',
                dataIndex: 'openai_base_url',
                copyable: true,
              },
              { title: 'Originator', dataIndex: 'openai_originator' },
              {
                title: 'User Agent',
                dataIndex: 'openai_user_agent',
                copyable: true,
              },
              { title: '客户端版本', dataIndex: 'openai_client_version' },
              {
                title: '版本最近检查',
                render: (_, record) =>
                  formatDateTime(record?.client_version_last_checked),
              },
              {
                title: '版本最近更新',
                render: (_, record) =>
                  formatDateTime(record?.client_version_last_updated),
              },
            ]}
          />
        </ProCard>

        <ProCard title="认证与调度">
          <ProDescriptions<Prism.Settings>
            loading={settingsReq.loading}
            dataSource={settings}
            column={2}
            columns={[
              {
                title: 'OAuth Client ID',
                dataIndex: 'oauth_client_id',
                copyable: true,
              },
              {
                title: 'OAuth Auth Endpoint',
                dataIndex: 'oauth_auth_endpoint',
                copyable: true,
              },
              {
                title: 'OAuth Token Endpoint',
                dataIndex: 'oauth_token_endpoint',
                copyable: true,
              },
              { title: '刷新并发', dataIndex: 'refresh_concurrency' },
              { title: '请求间隔 ms', dataIndex: 'request_interval_ms' },
              {
                title: '单账号最大并发',
                dataIndex: 'max_concurrent_per_account',
              },
              {
                title: 'Tier Priority',
                render: (_, record) => (
                  <Space wrap>
                    {(record?.tier_priority || []).map((item) => (
                      <Tag key={item}>{item}</Tag>
                    ))}
                    {!record?.tier_priority?.length ? '-' : null}
                  </Space>
                ),
              },
            ]}
          />
        </ProCard>
      </Space>
    </PageContainer>
  );
};

export default SettingsPage;
