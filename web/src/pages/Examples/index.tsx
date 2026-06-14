import { useRawRequest } from '@/hooks/useRawRequest';
import {
  getAuthStatus,
  getModelCatalog,
  getOpenAIModels,
  getSettings,
} from '@/services/api';
import { jsonStringify, maskSecret } from '@/utils/prism';
import { CopyOutlined, ReloadOutlined } from '@ant-design/icons';
import type { ProColumns } from '@ant-design/pro-components';
import {
  PageContainer,
  ProCard,
  ProDescriptions,
  ProTable,
} from '@ant-design/pro-components';
import { Button, Space, Tabs, Tag, Typography, message } from 'antd';
import React from 'react';
import styles from './index.less';

const copy = async (value: string) => {
  await navigator.clipboard.writeText(value);
  message.success('已复制');
};

const ExamplesPage: React.FC = () => {
  const authReq = useRawRequest(getAuthStatus);
  const settingsReq = useRawRequest(getSettings);
  const catalogReq = useRawRequest(getModelCatalog);
  const openAIModelsReq = useRawRequest(getOpenAIModels);

  const key = authReq.data?.proxy_api_key || '<PROXY_API_KEY>';
  const model =
    settingsReq.data?.default_model || catalogReq.data?.[0]?.id || 'gpt-5.4';

  const responsesPayload = {
    model,
    instructions: 'You are a concise assistant.',
    input: [{ role: 'user', content: 'Hello from Prism' }],
    reasoning: {
      effort: settingsReq.data?.default_reasoning_effort || 'medium',
      summary: 'auto',
    },
    stream: false,
    store: false,
  };

  const chatPayload = {
    model,
    messages: [
      { role: 'system', content: 'You are a concise assistant.' },
      { role: 'user', content: 'Hello from Chat Completions' },
    ],
    reasoning_effort: settingsReq.data?.default_reasoning_effort || 'medium',
    stream: false,
  };

  const responsesCurl = `curl http://localhost:8080/v1/responses \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonStringify(responsesPayload)}'`;

  const chatCurl = `curl http://localhost:8080/v1/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonStringify(chatPayload)}'`;

  const modelColumns: ProColumns<Prism.OpenAIModel>[] = [
    {
      title: 'ID',
      dataIndex: 'id',
      render: (_, record) => <Tag>{record.id}</Tag>,
    },
    { title: 'Object', dataIndex: 'object' },
    { title: 'Owned By', dataIndex: 'owned_by' },
    { title: 'Created', dataIndex: 'created' },
  ];

  return (
    <PageContainer
      title="API 示例"
      extra={[
        <Button
          key="refresh"
          icon={<ReloadOutlined />}
          onClick={() => {
            authReq.refresh();
            settingsReq.refresh();
            catalogReq.refresh();
            openAIModelsReq.refresh();
          }}
        >
          刷新
        </Button>,
      ]}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <ProCard title="当前接入参数">
          <ProDescriptions column={2}>
            <ProDescriptions.Item label="Base URL">
              <Typography.Text copyable>http://localhost:8080</Typography.Text>
            </ProDescriptions.Item>
            <ProDescriptions.Item label="Bearer Token">
              <Typography.Text copyable={{ text: key }}>
                {maskSecret(key)}
              </Typography.Text>
            </ProDescriptions.Item>
            <ProDescriptions.Item label="默认模型">
              {model}
            </ProDescriptions.Item>
            <ProDescriptions.Item label="默认 Reasoning">
              {settingsReq.data?.default_reasoning_effort || '-'}
            </ProDescriptions.Item>
          </ProDescriptions>
        </ProCard>

        <ProCard title="请求示例">
          <Tabs
            items={[
              {
                key: 'responses',
                label: 'Responses',
                children: (
                  <Space
                    direction="vertical"
                    style={{ width: '100%' }}
                    size={12}
                  >
                    <Button
                      icon={<CopyOutlined />}
                      onClick={() => copy(responsesCurl)}
                    >
                      复制 cURL
                    </Button>
                    <pre className={styles.snippet}>{responsesCurl}</pre>
                  </Space>
                ),
              },
              {
                key: 'chat',
                label: 'Chat Completions',
                children: (
                  <Space
                    direction="vertical"
                    style={{ width: '100%' }}
                    size={12}
                  >
                    <Button
                      icon={<CopyOutlined />}
                      onClick={() => copy(chatCurl)}
                    >
                      复制 cURL
                    </Button>
                    <pre className={styles.snippet}>{chatCurl}</pre>
                  </Space>
                ),
              },
              {
                key: 'payload',
                label: 'JSON Payload',
                children: (
                  <pre className={styles.snippet}>
                    {jsonStringify({
                      responses: responsesPayload,
                      chat: chatPayload,
                    })}
                  </pre>
                ),
              },
            ]}
          />
        </ProCard>

        <ProTable<Prism.OpenAIModel>
          rowKey="id"
          headerTitle="/v1/models"
          search={false}
          options={false}
          dataSource={openAIModelsReq.data?.data || []}
          columns={modelColumns}
          pagination={{ pageSize: 10 }}
        />
      </Space>
    </PageContainer>
  );
};

export default ExamplesPage;
