import { LogoutOutlined, ReloadOutlined } from '@ant-design/icons';
import type { RequestConfig } from '@umijs/max';
import { history } from '@umijs/max';
import { Button, message, Space, Tag, Typography } from 'antd';
import BrandLogo from './components/BrandLogo';
import {
  dashboardLogout,
  dashboardStatus,
  getAuthStatus,
} from './services/api';

export type InitialState = {
  authenticated: boolean;
  authStatus?: Prism.AuthStatus;
};

export async function getInitialState(): Promise<InitialState> {
  const [dashboard, authStatus] = await Promise.allSettled([
    dashboardStatus(),
    getAuthStatus(),
  ]);

  return {
    authenticated:
      dashboard.status === 'fulfilled' && !!dashboard.value.authenticated,
    authStatus:
      authStatus.status === 'fulfilled' ? authStatus.value : undefined,
  };
}

const loginPath = '/login';

const buildRedirectLoginUrl = () => {
  const { pathname, search, hash } = history.location;
  return `${loginPath}?redirect=${encodeURIComponent(
    pathname + search + hash,
  )}`;
};

export const request: RequestConfig = {
  timeout: 30000,
  withCredentials: true,
  errorConfig: {
    errorHandler: (error: any) => {
      const status = error?.response?.status;
      const data = error?.response?.data;
      const rawMessage =
        data?.error?.message || data?.error || error?.message || '请求失败';
      if (status === 401 && history.location.pathname !== loginPath) {
        history.replace(buildRedirectLoginUrl());
        return;
      }
      message.error(rawMessage);
      throw new Error(rawMessage);
    },
  },
};

export const layout = ({
  initialState,
  setInitialState,
}: {
  initialState?: InitialState;
  setInitialState: (state: InitialState) => void;
}) => {
  return {
    logo: <BrandLogo compact />,
    menu: {
      locale: false,
    },
    layout: 'mix' as const,
    contentWidth: 'Fluid' as const,
    fixedHeader: true,
    token: {
      sider: {
        colorMenuBackground: '#fff',
      },
      header: {
        colorBgHeader: '#fff',
      },
    },
    rightContentRender: () => (
      <Space size={12}>
        <Tag color={initialState?.authenticated ? 'green' : 'default'}>
          {initialState?.authenticated ? '已登录' : '未登录'}
        </Tag>
        <Typography.Text type="secondary">
          {initialState?.authStatus?.pool?.active ?? 0} active
        </Typography.Text>
        <Button
          icon={<ReloadOutlined />}
          size="small"
          onClick={async () => {
            const [dashboard, authStatus] = await Promise.all([
              dashboardStatus(),
              getAuthStatus(),
            ]);
            setInitialState({
              authenticated: dashboard.authenticated,
              authStatus,
            });
          }}
        />
        <Button
          icon={<LogoutOutlined />}
          size="small"
          onClick={async () => {
            await dashboardLogout().catch(() => undefined);
            setInitialState({
              authenticated: false,
              authStatus: initialState?.authStatus,
            });
            history.push('/login');
          }}
        >
          退出
        </Button>
      </Space>
    ),
    onPageChange: () => {
      const { pathname } = history.location;
      if (!initialState?.authenticated && pathname !== loginPath) {
        history.replace(buildRedirectLoginUrl());
      }
    },
  };
};
