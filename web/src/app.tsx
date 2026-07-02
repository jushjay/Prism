import { BulbFilled, BulbOutlined, LogoutOutlined, ReloadOutlined } from '@ant-design/icons';
import type { RequestConfig } from '@umijs/max';
import { history } from '@umijs/max';
import {
  Button,
  ConfigProvider,
  message,
  Space,
  Tag,
  Tooltip,
  Typography,
  theme as antdTheme,
} from 'antd';
import zhCN from 'antd/locale/zh_CN';
import React from 'react';
import BrandLogo from './components/BrandLogo';
import {
  dashboardLogout,
  dashboardStatus,
  getAuthStatus,
} from './services/api';
import { toggleTheme, useTheme } from './utils/themeStore';
import './global.less';

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

const RootWrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const theme = useTheme();
  const isDark = theme === 'dark';
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: isDark
          ? antdTheme.darkAlgorithm
          : antdTheme.defaultAlgorithm,
        token: {
          colorPrimary: '#2f8bff',
          colorInfo: '#2f8bff',
          colorBgBase: isDark ? '#0b1020' : '#ffffff',
          borderRadius: 8,
          wireframe: false,
        },
        components: {
          Layout: {
            headerBg: 'transparent',
            siderBg: 'transparent',
            bodyBg: 'transparent',
          },
          Card: { colorBgContainer: 'transparent' },
          Menu: isDark
            ? {
                darkItemBg: 'transparent',
                darkSubMenuItemBg: 'transparent',
                darkItemSelectedBg: 'rgba(47,139,255,0.18)',
                darkItemHoverBg: 'rgba(255,255,255,0.06)',
              }
            : {
                itemSelectedBg: 'rgba(47,139,255,0.12)',
                itemHoverBg: 'rgba(47,139,255,0.06)',
              },
        },
      }}
    >
      {children}
    </ConfigProvider>
  );
};

export const rootContainer = (container: React.ReactNode) => (
  <RootWrapper>{container}</RootWrapper>
);

export const layout = ({
  initialState,
  setInitialState,
}: {
  initialState?: InitialState;
  setInitialState: (state: InitialState) => void;
}) => {
  const theme = useTheme();
  const isDark = theme === 'dark';
  return {
    logo: <BrandLogo compact />,
    menu: {
      locale: false,
    },
    layout: 'mix' as const,
    navTheme: (isDark ? 'realDark' : 'light') as 'realDark' | 'light',
    contentWidth: 'Fluid' as const,
    fixedHeader: true,
    token: {
      header: {
        colorBgHeader: isDark
          ? 'rgba(11,16,32,0.72)'
          : 'rgba(255,255,255,0.82)',
        colorTextRightActionsItem: isDark
          ? 'rgba(235,245,255,0.85)'
          : 'rgba(24,33,58,0.85)',
      },
      sider: {
        colorMenuBackground: isDark
          ? 'rgba(11,16,32,0.82)'
          : 'rgba(255,255,255,0.86)',
        colorBgMenuItemSelected: 'rgba(47,139,255,0.18)',
        colorTextMenuSelected: isDark ? '#fff' : '#2f8bff',
      },
      pageContainer: {
        colorBgPageContainer: 'transparent',
      },
    },
    rightContentRender: () => (
      <Space size={12} align="center">
        <Tooltip title={isDark ? '切换到亮色主题' : '切换到暗色主题'}>
          <Button
            size="small"
            icon={isDark ? <BulbOutlined /> : <BulbFilled />}
            onClick={toggleTheme}
          />
        </Tooltip>
        <span
          className="tech-status-dot"
          data-on={initialState?.authenticated ? 1 : 0}
        />
        <Tag
          color={initialState?.authenticated ? 'success' : 'default'}
          bordered={false}
        >
          {initialState?.authenticated ? '在线' : '离线'}
        </Tag>
        <Typography.Text
          style={{
            color: isDark
              ? 'rgba(190,210,240,0.6)'
              : 'rgba(80,100,140,0.7)',
            fontVariantNumeric: 'tabular-nums',
          }}
        >
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
