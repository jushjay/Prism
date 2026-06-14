import { Footer } from '@/components';
import BrandLogo from '@/components/BrandLogo';
import { dashboardLogin } from '@/services/api';
import { LockOutlined } from '@ant-design/icons';
import { LoginForm, ProFormText } from '@ant-design/pro-components';
import { Helmet, useModel } from '@umijs/max';
import { message } from 'antd';
import { createStyles } from 'antd-style';
import React from 'react';

const useStyles = createStyles(({ token }) => {
  return {
    container: {
      display: 'flex',
      flexDirection: 'column',
      minHeight: '100vh',
      overflow: 'auto',
      backgroundImage:
        "url('https://mdn.alipayobjects.com/yuyan_qk0oxh/afts/img/V-_oS6r-i7wAAAAAAAAAAAAAFl94AQBr')",
      backgroundSize: '100% 100%',
      backgroundPosition: 'center',
    },
    loginShell: {
      flex: '1',
      display: 'grid',
      placeItems: 'center',
      padding: '48px 24px',
    },
    loginPanel: {
      width: '100%',
      maxWidth: 420,
    },
    brandHeader: {
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      gap: 12,
      marginBottom: 28,
      textAlign: 'center',
    },
    brandSubtitle: {
      color: token.colorTextDescription,
      fontSize: token.fontSize,
      letterSpacing: '0.08em',
      textTransform: 'uppercase' as const,
    },
  };
});

const LoginPage: React.FC = () => {
  const { setInitialState } = useModel('@@initialState');
  const { styles } = useStyles();

  const getSafeRedirectUrl = (redirect: string | null): string => {
    if (!redirect?.startsWith('/')) return '/overview';
    if (redirect.startsWith('//')) return '/overview';

    try {
      const parsed = new URL(redirect, window.location.origin);
      if (parsed.origin !== window.location.origin) return '/overview';
      return `${parsed.pathname}${parsed.search}${parsed.hash}`;
    } catch {
      return '/overview';
    }
  };

  const handleSubmit = async (values: { password: string }) => {
    try {
      await dashboardLogin(values.password);
      setInitialState((state) => ({
        ...state,
        authenticated: true,
      }));
      message.success('登录成功');

      const urlParams = new URL(window.location.href).searchParams;
      const redirectUrl = getSafeRedirectUrl(urlParams.get('redirect'));
      window.location.href = redirectUrl;
      return true;
    } catch (error) {
      message.error('登录失败，请重试');
      return false;
    }
  };

  return (
    <div className={styles.container}>
      <Helmet>
        <title>登录页 - Prism</title>
      </Helmet>
      <div className={styles.loginShell}>
        <div className={styles.loginPanel}>
          <div className={styles.brandHeader}>
            <BrandLogo />
            <div className={styles.brandSubtitle}>Dashboard Access</div>
          </div>
          <LoginForm
            contentStyle={{
              width: '100%',
              minWidth: 0,
              maxWidth: '100%',
            }}
            onFinish={async (values) => {
              return handleSubmit(values as { password: string });
            }}
          >
            <ProFormText.Password
              name="password"
              fieldProps={{
                size: 'large',
                prefix: <LockOutlined />,
              }}
              placeholder="PROXY_API_KEY"
              rules={[
                {
                  required: true,
                  message: '请输入登录密钥！',
                },
              ]}
            />
          </LoginForm>
        </div>
      </div>
      <Footer />
    </div>
  );
};

export default LoginPage;
