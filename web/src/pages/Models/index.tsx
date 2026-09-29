import { useRawRequest } from '@/hooks/useRawRequest';
import {
  deleteModelMapping,
  deleteModelWhitelist,
  listAccounts,
  listCustomAccountModels,
  listModelMappings,
  listModelReasoningDefaults,
  listModelWhitelists,
  refreshAccountModels,
  upsertModelMapping,
  upsertModelReasoningDefault,
  upsertModelWhitelist,
} from '@/services/api';
import {
  displayAccount,
  formatDateTime,
  maskUsageAccountDisplay,
} from '@/utils/prism';
import {
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import type { ActionType, ProColumns } from '@ant-design/pro-components';
import {
  ModalForm,
  PageContainer,
  ProDescriptions,
  ProFormDependency,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
  ProTable,
} from '@ant-design/pro-components';
import {
  Button,
  Drawer,
  Popconfirm,
  Select,
  Space,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import React, { useMemo, useRef, useState } from 'react';

const ModelsPage: React.FC = () => {
  const catalogActionRef = useRef<ActionType>();
  const mappingActionRef = useRef<ActionType>();
  const whitelistActionRef = useRef<ActionType>();
  const reasoningDefaultActionRef = useRef<ActionType>();
  const [activeTab, setActiveTab] = useState('catalog');
  const [mappingOpen, setMappingOpen] = useState(false);
  const [editingMapping, setEditingMapping] = useState<Prism.ModelMapping>();
  const [whitelistOpen, setWhitelistOpen] = useState(false);
  const [editingWhitelist, setEditingWhitelist] =
    useState<Prism.ModelWhitelist>();
  const [reasoningDefaultOpen, setReasoningDefaultOpen] = useState(false);
  const [editingReasoningDefault, setEditingReasoningDefault] =
    useState<Prism.ModelReasoningDefault>();
  const [selectedAccountId, setSelectedAccountId] = useState<string>();
  const [modelDetail, setModelDetail] =
    useState<Prism.CustomAccountModelRecord>();

  const modelAccountLabel = (item?: {
    provider?: string;
    account_label?: string;
    account_email?: string;
    account_id?: string;
  }) =>
    maskUsageAccountDisplay({
      account_provider: item?.provider,
      account_display_name:
        item?.account_label || item?.account_email || item?.account_id || '-',
      account_label: item?.account_label,
      account_email: item?.account_email,
    });

  const accountsReq = useRawRequest(listAccounts);

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

  const accountOptionMap = useMemo(
    () => new Map(accountOptions.map((option) => [option.value, option.label])),
    [accountOptions],
  );

  const whitelistAccountLabel = (accountId: string) =>
    accountOptionMap.get(accountId) || `${accountId.slice(0, 8)} (账号不存在)`;

  const catalogColumns: ProColumns<Prism.CustomAccountModelRecord>[] = [
    {
      title: '账号',
      dataIndex: 'account_id',
      width: 220,
      hideInSearch: true,
      render: (_, record) => (
        <Space direction="vertical" size={2}>
          <Space size={6}>
            <Typography.Text>{modelAccountLabel(record)}</Typography.Text>
            <Tag color={record.provider === 'openai' ? 'geekblue' : 'gold'}>
              {record.provider === 'openai' ? 'OpenAI' : 'Custom'}
            </Tag>
          </Space>
          <Typography.Text type="secondary">
            {record.account_id.slice(0, 8)}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: '模型',
      dataIndex: 'model',
      width: 260,
      ellipsis: true,
      fieldProps: {
        placeholder: '模型 ID / 展示名 / Owner',
      },
      render: (_, record) => (
        <Space direction="vertical" size={2}>
          <Typography.Link onClick={() => setModelDetail(record)}>
            {record.model_id}
          </Typography.Link>
          <Typography.Text type="secondary">
            {record.display_name || '-'}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: 'Object',
      dataIndex: 'object',
      width: 110,
      hideInSearch: true,
      render: (_, record) => record.object || '-',
    },
    {
      title: 'Owner',
      dataIndex: 'owned_by',
      width: 180,
      hideInSearch: true,
      render: (_, record) => record.owned_by || '-',
    },
    {
      title: 'Created',
      dataIndex: 'created',
      width: 170,
      hideInSearch: true,
      render: (_, record) =>
        record.created ? formatDateTime(record.created * 1000) : '-',
    },
    {
      title: '同步时间',
      dataIndex: 'fetched_at',
      width: 170,
      hideInSearch: true,
      render: (_, record) => formatDateTime(record.fetched_at),
    },
    {
      title: '过期时间',
      dataIndex: 'expires_at',
      width: 170,
      hideInSearch: true,
      render: (_, record) => formatDateTime(record.expires_at),
    },
    {
      title: '最近错误',
      dataIndex: 'last_error',
      hideInSearch: true,
      ellipsis: true,
      render: (_, record) => record.last_error || '-',
    },
  ];

  const mappingColumns: ProColumns<Prism.ModelMapping>[] = [
    {
      title: '对外模型名',
      dataIndex: 'model_name',
      render: (_, record) => <Tag>{record.model_name}</Tag>,
    },
    {
      title: '目标模型',
      dataIndex: 'target_model',
      render: (_, record) => <Tag color="blue">{record.target_model}</Tag>,
    },
    {
      title: '思考强度',
      dataIndex: 'reasoning_effort',
      render: (_, record) =>
        record.reasoning_effort ? (
          <Tag color="purple">{record.reasoning_effort}</Tag>
        ) : (
          '-'
        ),
    },
    {
      title: '作用域',
      dataIndex: 'apply_global',
      render: (_, record) =>
        record.apply_global ? (
          <Tag color="green">全局</Tag>
        ) : (
          modelAccountLabel({
            provider: 'openai',
            account_label: '',
            account_email: record.account_email,
            account_id: record.account_id,
          })
        ),
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      render: (_, record) => formatDateTime(record.updated_at),
    },
    {
      title: '操作',
      valueType: 'option',
      width: 140,
      render: (_, record) => (
        <Space>
          <Button
            size="small"
            icon={<EditOutlined />}
            onClick={() => {
              setEditingMapping(record);
              setMappingOpen(true);
            }}
          />
          <Popconfirm
            title="删除模型映射"
            onConfirm={async () => {
              await deleteModelMapping(record.record_id);
              message.success('映射已删除');
              mappingActionRef.current?.reload();
            }}
          >
            <Button danger size="small" icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const reasoningDefaultColumns: ProColumns<Prism.ModelReasoningDefault>[] = [
    {
      title: '模型',
      dataIndex: 'model_id',
      width: 280,
      fieldProps: {
        placeholder: '模型 ID / 展示名',
      },
      render: (_, record) => (
        <Space direction="vertical" size={2}>
          <Typography.Text strong>{record.model_id}</Typography.Text>
          <Typography.Text type="secondary">
            {record.display_name || record.model_id}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: '默认思考强度',
      dataIndex: 'default_reasoning_effort',
      width: 200,
      render: (_, record) =>
        record.default_reasoning_effort ? (
          <Space size={6} wrap>
            <Tag color="purple">{record.default_reasoning_effort}</Tag>
            {record.configured ? <Tag color="blue">页面配置</Tag> : null}
          </Space>
        ) : (
          <Typography.Text type="secondary">未设置</Typography.Text>
        ),
    },
    {
      title: '支持的思考强度',
      dataIndex: 'supported_reasoning_efforts',
      render: (_, record) =>
        record.supported_reasoning_efforts.length ? (
          <Space size={[4, 4]} wrap>
            {record.supported_reasoning_efforts.map((item) => (
              <Tag key={item.reasoningEffort}>{item.reasoningEffort}</Tag>
            ))}
          </Space>
        ) : (
          <Typography.Text type="secondary">未提供</Typography.Text>
        ),
    },
    {
      title: '操作',
      valueType: 'option',
      width: 120,
      render: (_, record) => (
        <Button
          size="small"
          title="配置默认思考强度"
          icon={<EditOutlined />}
          onClick={() => {
            setEditingReasoningDefault(record);
            setReasoningDefaultOpen(true);
          }}
        />
      ),
    },
  ];

  const whitelistColumns: ProColumns<Prism.ModelWhitelist>[] = [
    {
      title: '模型',
      dataIndex: 'model_name',
      width: 280,
      render: (_, record) => (
        <Typography.Text strong copyable>
          {record.model_name}
        </Typography.Text>
      ),
    },
    {
      title: '允许账号',
      dataIndex: 'account_ids',
      render: (_, record) => (
        <Space size={[4, 4]} wrap>
          {record.account_ids.map((accountId) => (
            <Tag key={accountId} color="blue">
              {whitelistAccountLabel(accountId)}
            </Tag>
          ))}
        </Space>
      ),
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 180,
      render: (_, record) => formatDateTime(record.updated_at),
    },
    {
      title: '操作',
      valueType: 'option',
      width: 140,
      render: (_, record) => (
        <Space>
          <Button
            size="small"
            title="编辑模型白名单"
            icon={<EditOutlined />}
            onClick={() => {
              setEditingWhitelist(record);
              setWhitelistOpen(true);
            }}
          />
          <Popconfirm
            title="删除模型白名单"
            onConfirm={async () => {
              await deleteModelWhitelist(record.record_id);
              message.success('模型白名单已删除');
              whitelistActionRef.current?.reload();
            }}
          >
            <Button
              danger
              size="small"
              title="删除模型白名单"
              icon={<DeleteOutlined />}
            />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const loadAccountModels = async (accountId: string) => {
    await refreshAccountModels(accountId);
    message.success('账号模型已同步');
    catalogActionRef.current?.reload();
  };

  return (
    <PageContainer title="模型管理">
      <Tabs
        activeKey={activeTab}
        onChange={setActiveTab}
        items={[
          {
            key: 'catalog',
            label: '账号模型目录',
            children: (
              <ProTable<Prism.CustomAccountModelRecord>
                actionRef={catalogActionRef}
                rowKey={(record) => `${record.account_id}-${record.model_id}`}
                headerTitle="账号模型目录"
                columns={catalogColumns}
                search={{
                  labelWidth: 88,
                  defaultCollapsed: false,
                }}
                options={false}
                params={{ selectedAccountId }}
                toolBarRender={() => [
                  <Select
                    key="account"
                    showSearch
                    allowClear
                    style={{ width: 280 }}
                    placeholder="全部账号 / 指定账号"
                    options={accountOptions}
                    value={selectedAccountId}
                    onChange={(value) => {
                      setSelectedAccountId(value);
                      catalogActionRef.current?.reload();
                      mappingActionRef.current?.reload();
                    }}
                  />,
                  <Button
                    key="refresh"
                    icon={<ReloadOutlined />}
                    disabled={!selectedAccountId}
                    onClick={() =>
                      selectedAccountId && loadAccountModels(selectedAccountId)
                    }
                  >
                    强制刷新
                  </Button>,
                ]}
                request={async (params) => {
                  const data = await listCustomAccountModels({
                    account_id: selectedAccountId,
                    model:
                      typeof params.model === 'string'
                        ? params.model.trim()
                        : undefined,
                  });
                  return {
                    data,
                    success: true,
                    total: data.length,
                  };
                }}
                pagination={{ pageSize: 10 }}
                scroll={{ x: 1280 }}
              />
            ),
          },
          {
            key: 'mapping',
            label: '模型映射',
            children: (
              <ProTable<Prism.ModelMapping>
                actionRef={mappingActionRef}
                rowKey="record_id"
                headerTitle="模型映射"
                columns={mappingColumns}
                search={false}
                toolBarRender={() => [
                  <Select
                    key="account"
                    showSearch
                    allowClear
                    style={{ width: 280 }}
                    placeholder="全部账号 / 指定账号"
                    options={accountOptions}
                    value={selectedAccountId}
                    onChange={(value) => {
                      setSelectedAccountId(value);
                      catalogActionRef.current?.reload();
                      mappingActionRef.current?.reload();
                    }}
                  />,
                  <Button
                    key="mapping"
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => {
                      setEditingMapping(undefined);
                      setMappingOpen(true);
                    }}
                  >
                    新增映射
                  </Button>,
                ]}
                request={async () => {
                  const data = await listModelMappings(selectedAccountId);
                  return { data, success: true, total: data.length };
                }}
              />
            ),
          },
          {
            key: 'whitelist',
            label: '模型白名单',
            children: (
              <ProTable<Prism.ModelWhitelist>
                actionRef={whitelistActionRef}
                rowKey="record_id"
                headerTitle="模型白名单"
                columns={whitelistColumns}
                search={false}
                options={false}
                toolBarRender={() => [
                  <Button
                    key="whitelist"
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => {
                      setEditingWhitelist(undefined);
                      setWhitelistOpen(true);
                    }}
                  >
                    新增白名单
                  </Button>,
                ]}
                request={async () => {
                  const data = await listModelWhitelists();
                  return { data, success: true, total: data.length };
                }}
                pagination={{ pageSize: 10 }}
              />
            ),
          },
          {
            key: 'reasoning-default',
            label: '默认思考强度',
            children: (
              <ProTable<Prism.ModelReasoningDefault>
                actionRef={reasoningDefaultActionRef}
                rowKey="model_id"
                headerTitle="默认思考强度"
                columns={reasoningDefaultColumns}
                search={{
                  labelWidth: 96,
                  defaultCollapsed: false,
                }}
                toolBarRender={() => [
                  <Button
                    key="reasoning-default"
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => {
                      setEditingReasoningDefault(undefined);
                      setReasoningDefaultOpen(true);
                    }}
                  >
                    新增配置
                  </Button>,
                ]}
                request={async (params) => {
                  const data = await listModelReasoningDefaults();
                  const keyword =
                    typeof params.model_id === 'string'
                      ? params.model_id.trim().toLowerCase()
                      : '';
                  const filtered = keyword
                    ? data.filter((item) =>
                        [item.model_id, item.display_name].some((value) =>
                          value.toLowerCase().includes(keyword),
                        ),
                      )
                    : data;
                  return {
                    data: filtered,
                    success: true,
                    total: filtered.length,
                  };
                }}
                pagination={{ pageSize: 10 }}
              />
            ),
          },
        ]}
      />

      <ModalForm
        title={editingWhitelist ? '编辑模型白名单' : '新增模型白名单'}
        open={whitelistOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => {
            setWhitelistOpen(false);
            setEditingWhitelist(undefined);
          },
        }}
        initialValues={
          editingWhitelist
            ? {
                modelName: editingWhitelist.model_name,
                accountIds: editingWhitelist.account_ids,
              }
            : undefined
        }
        onFinish={async (values) => {
          await upsertModelWhitelist({
            recordId: editingWhitelist?.record_id,
            modelName: values.modelName,
            accountIds: values.accountIds || [],
          });
          message.success('模型白名单已保存');
          setWhitelistOpen(false);
          setEditingWhitelist(undefined);
          whitelistActionRef.current?.reload();
          return true;
        }}
      >
        <ProFormText
          name="modelName"
          label="模型 ID"
          disabled={!!editingWhitelist}
          rules={[{ required: true, message: '请输入模型 ID' }]}
          extra="按请求中的模型 ID 匹配。"
        />
        <ProFormSelect
          name="accountIds"
          label="允许账号"
          mode="multiple"
          showSearch
          options={accountOptions}
          rules={[{ required: true, message: '至少选择一个账号' }]}
          extra="配置后，该模型只会路由到这里选择的账号。"
        />
      </ModalForm>

      <ModalForm
        title={editingMapping ? '编辑模型映射' : '新增模型映射'}
        open={mappingOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => setMappingOpen(false),
        }}
        initialValues={
          editingMapping
            ? {
                modelName: editingMapping.model_name,
                targetModel: editingMapping.target_model,
                reasoningEffort: editingMapping.reasoning_effort,
                applyGlobal: editingMapping.apply_global,
                accountId: editingMapping.account_id,
              }
            : { applyGlobal: true }
        }
        onFinish={async (values) => {
          await upsertModelMapping({
            recordId: editingMapping?.record_id,
            modelName: values.modelName,
            targetModel: values.targetModel,
            reasoningEffort: values.reasoningEffort,
            applyGlobal: values.applyGlobal,
            accountId: values.accountId,
          });
          message.success('模型映射已保存');
          setMappingOpen(false);
          setEditingMapping(undefined);
          mappingActionRef.current?.reload();
          return true;
        }}
      >
        <ProFormText
          name="modelName"
          label="对外模型名"
          rules={[{ required: true, message: '请输入对外模型名' }]}
        />
        <ProFormText
          name="targetModel"
          label="目标模型"
          rules={[{ required: true, message: '请输入目标模型 ID' }]}
          extra="目标模型来自账号模型目录中的 model_id。支持直接输入。"
        />
        <ProFormSelect
          name="reasoningEffort"
          label="思考强度"
          allowClear
          options={[
            { label: 'low', value: 'low' },
            { label: 'medium', value: 'medium' },
            { label: 'high', value: 'high' },
            { label: 'xhigh', value: 'xhigh' },
            { label: 'max', value: 'max' },
          ]}
          extra="可选。为空时沿用请求显式传参、模型映射、目标模型默认值或系统默认值。"
        />
        <ProFormSwitch name="applyGlobal" label="全局生效" />
        <ProFormDependency name={['applyGlobal']}>
          {({ applyGlobal }) =>
            applyGlobal ? null : (
              <ProFormSelect
                name="accountId"
                label="指定账号"
                showSearch
                options={accountOptions}
                rules={[{ required: true, message: '请选择账号' }]}
              />
            )
          }
        </ProFormDependency>
      </ModalForm>

      <ModalForm
        title={
          editingReasoningDefault ? '编辑默认思考强度' : '新增默认思考强度'
        }
        open={reasoningDefaultOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => {
            setReasoningDefaultOpen(false);
            setEditingReasoningDefault(undefined);
          },
        }}
        initialValues={
          editingReasoningDefault
            ? {
                modelId: editingReasoningDefault.model_id,
                reasoningEffort: editingReasoningDefault.configured
                  ? editingReasoningDefault.default_reasoning_effort
                  : undefined,
              }
            : undefined
        }
        onFinish={async (values) => {
          await upsertModelReasoningDefault({
            modelId: editingReasoningDefault?.model_id || values.modelId,
            reasoningEffort: values.reasoningEffort,
          });
          message.success(
            values.reasoningEffort
              ? '默认思考强度已保存'
              : '已恢复模型目录默认值',
          );
          setReasoningDefaultOpen(false);
          setEditingReasoningDefault(undefined);
          reasoningDefaultActionRef.current?.reload();
          return true;
        }}
      >
        <ProFormText
          name="modelId"
          label="模型 ID"
          disabled={!!editingReasoningDefault}
          rules={[{ required: true, message: '请输入模型 ID' }]}
        />
        <ProFormSelect
          name="reasoningEffort"
          label="默认思考强度"
          allowClear
          options={[
            { label: 'low', value: 'low' },
            { label: 'medium', value: 'medium' },
            { label: 'high', value: 'high' },
            { label: 'xhigh', value: 'xhigh' },
            { label: 'max', value: 'max' },
          ]}
          extra="清空后恢复模型目录或上游返回的默认值。"
        />
      </ModalForm>

      <Drawer
        title="模型详情"
        open={!!modelDetail}
        width={680}
        onClose={() => setModelDetail(undefined)}
      >
        {modelDetail ? (
          <ProDescriptions
            column={1}
            dataSource={modelDetail}
            columns={[
              { title: '账号 ID', dataIndex: 'account_id', copyable: true },
              {
                title: '账号',
                render: () => modelAccountLabel(modelDetail),
              },
              { title: '模型 ID', dataIndex: 'model_id', copyable: true },
              { title: '展示名', dataIndex: 'display_name' },
              { title: 'Object', dataIndex: 'object' },
              { title: 'Owner', dataIndex: 'owned_by' },
              {
                title: 'Created',
                render: (_, record) =>
                  record.created ? formatDateTime(record.created * 1000) : '-',
              },
              {
                title: '同步时间',
                render: (_, record) => formatDateTime(record.fetched_at),
              },
              {
                title: '过期时间',
                render: (_, record) => formatDateTime(record.expires_at),
              },
              { title: '最近错误', dataIndex: 'last_error' },
            ]}
          />
        ) : null}
      </Drawer>
    </PageContainer>
  );
};

export default ModelsPage;
