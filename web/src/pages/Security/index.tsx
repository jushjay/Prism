import { useRawRequest } from '@/hooks/useRawRequest';
import {
  deleteIPRule,
  getIPFilter,
  setIPFilterEnabled,
  upsertIPRule,
} from '@/services/api';
import { formatDateTime, formatNumber } from '@/utils/prism';
import {
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import type { ProColumns } from '@ant-design/pro-components';
import {
  ModalForm,
  PageContainer,
  ProCard,
  ProFormRadio,
  ProFormText,
  ProTable,
  StatisticCard,
} from '@ant-design/pro-components';
import {
  Button,
  Col,
  Popconfirm,
  Row,
  Space,
  Switch,
  Tag,
  message,
} from 'antd';
import React, { useState } from 'react';

const SecurityPage: React.FC = () => {
  const [ruleOpen, setRuleOpen] = useState(false);
  const filterReq = useRawRequest(getIPFilter);

  const reload = () => filterReq.refresh();

  const ruleColumns: ProColumns<Prism.IPRule>[] = [
    {
      title: '规则',
      dataIndex: 'value',
      render: (_, record) => <Tag>{record.value}</Tag>,
    },
    {
      title: '类型',
      dataIndex: 'match_type',
      width: 120,
      render: (_, record) => record.match_type.toUpperCase(),
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
      width: 96,
      render: (_, record) => (
        <Popconfirm
          title="删除 IP 规则"
          onConfirm={async () => {
            await deleteIPRule(record.id);
            message.success('规则已删除');
            reload();
          }}
        >
          <Button danger size="small" icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ];

  const accessColumns: ProColumns<Prism.AccessStat>[] = [
    {
      title: 'IP',
      dataIndex: 'ip',
      render: (_, record) => <Tag>{record.ip}</Tag>,
    },
    {
      title: '请求数',
      dataIndex: 'request_count',
      sorter: (a, b) => a.request_count - b.request_count,
      render: (_, record) => formatNumber(record.request_count),
    },
    {
      title: '拒绝数',
      dataIndex: 'denied_count',
      sorter: (a, b) => a.denied_count - b.denied_count,
      render: (_, record) => formatNumber(record.denied_count),
    },
    {
      title: '最近路径',
      dataIndex: 'last_path',
      ellipsis: true,
    },
    {
      title: '最近访问',
      dataIndex: 'last_seen_at',
      render: (_, record) => formatDateTime(record.last_seen_at),
    },
  ];

  const overview = filterReq.data;

  return (
    <PageContainer
      title="IP 防火墙"
      extra={[
        <Button key="reload" icon={<ReloadOutlined />} onClick={reload}>
          刷新
        </Button>,
        <Button
          key="new"
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setRuleOpen(true)}
        >
          新增规则
        </Button>,
      ]}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Row gutter={[16, 16]}>
          <Col xs={24} md={8} style={{ display: 'flex' }}>
            <StatisticCard
              style={{ width: '100%' }}
              loading={filterReq.loading}
              statistic={{
                title: '防火墙状态',
                value: overview?.enabled ? '已启用' : '未启用',
              }}
              footer={
                <Switch
                  checked={!!overview?.enabled}
                  checkedChildren="启用"
                  unCheckedChildren="关闭"
                  onChange={async (checked) => {
                    await setIPFilterEnabled(checked);
                    message.success(
                      checked ? 'IP 防火墙已启用' : 'IP 防火墙已关闭',
                    );
                    reload();
                  }}
                />
              }
            />
          </Col>
          <Col xs={24} md={8} style={{ display: 'flex' }}>
            <StatisticCard
              style={{ width: '100%' }}
              loading={filterReq.loading}
              statistic={{
                title: '来源 IP',
                value: overview?.source_summary?.unique_ips || 0,
                suffix: ` / ${formatNumber(
                  overview?.source_summary?.total_requests,
                )} requests`,
              }}
            />
          </Col>
          <Col xs={24} md={8} style={{ display: 'flex' }}>
            <StatisticCard
              style={{ width: '100%' }}
              loading={filterReq.loading}
              statistic={{
                title: '拒绝 IP',
                value: overview?.denied_summary?.unique_ips || 0,
                suffix: ` / ${formatNumber(
                  overview?.denied_summary?.total_denied_count,
                )} denied`,
              }}
            />
          </Col>
        </Row>

        <Row gutter={[16, 16]}>
          <Col xs={24} lg={12}>
            <ProCard title="白名单">
              <ProTable<Prism.IPRule>
                rowKey="id"
                search={false}
                options={false}
                dataSource={overview?.whitelist_rules || []}
                columns={ruleColumns}
                pagination={{ pageSize: 8 }}
              />
            </ProCard>
          </Col>
          <Col xs={24} lg={12}>
            <ProCard title="黑名单">
              <ProTable<Prism.IPRule>
                rowKey="id"
                search={false}
                options={false}
                dataSource={overview?.blacklist_rules || []}
                columns={ruleColumns}
                pagination={{ pageSize: 8 }}
              />
            </ProCard>
          </Col>
        </Row>

        <Row gutter={[16, 16]}>
          <Col xs={24} lg={12} style={{ display: 'flex' }}>
            <ProCard title="访问来源 Top 50" style={{ width: '100%' }}>
              <ProTable<Prism.AccessStat>
                rowKey="ip"
                search={false}
                options={false}
                dataSource={overview?.top_sources || []}
                columns={accessColumns}
                pagination={{ pageSize: 10 }}
              />
            </ProCard>
          </Col>
          <Col xs={24} lg={12} style={{ display: 'flex' }}>
            <ProCard title="拒绝来源 Top 50" style={{ width: '100%' }}>
              <ProTable<Prism.AccessStat>
                rowKey="ip"
                search={false}
                options={false}
                dataSource={overview?.top_denied || []}
                columns={accessColumns}
                pagination={{ pageSize: 10 }}
              />
            </ProCard>
          </Col>
        </Row>
      </Space>

      <ModalForm
        title="新增或更新 IP 规则"
        open={ruleOpen}
        modalProps={{
          destroyOnClose: true,
          onCancel: () => setRuleOpen(false),
        }}
        onFinish={async (values) => {
          await upsertIPRule(values as Prism.IPRulePayload);
          message.success('规则已保存');
          setRuleOpen(false);
          reload();
          return true;
        }}
      >
        <ProFormRadio.Group
          name="listType"
          label="规则列表"
          initialValue="whitelist"
          options={[
            { label: '白名单', value: 'whitelist' },
            { label: '黑名单', value: 'blacklist' },
          ]}
        />
        <ProFormText
          name="value"
          label="IP / CIDR"
          rules={[{ required: true, message: '请输入 IP 或 CIDR' }]}
          placeholder="203.0.113.7 或 203.0.113.0/24"
        />
      </ModalForm>
    </PageContainer>
  );
};

export default SecurityPage;
