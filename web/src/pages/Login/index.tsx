import { Footer } from '@/components';
import BrandLogo from '@/components/BrandLogo';
import { dashboardLogin } from '@/services/api';
import { LockOutlined } from '@ant-design/icons';
import { LoginForm, ProFormText } from '@ant-design/pro-components';
import { Helmet, useModel } from '@umijs/max';
import { message } from 'antd';
import { createStyles } from 'antd-style';
import React from 'react';

const useStyles = createStyles(({ css }) => {
  return {
    container: css`
      position: relative;
      display: flex;
      flex-direction: column;
      min-height: 100vh;
      overflow: hidden;
      background:
        radial-gradient(
          1000px 560px at 18% -10%,
          var(--glow-1),
          transparent 60%
        ),
        radial-gradient(
          820px 520px at 100% 10%,
          var(--glow-2),
          transparent 55%
        ),
        linear-gradient(180deg, var(--bg-page) 0%, var(--bg-page-2) 100%);
    `,
    containerOverlay: css`
      position: absolute;
      inset: 0;
      pointer-events: none;
      background-image:
        linear-gradient(var(--grid-line) 1px, transparent 1px),
        linear-gradient(90deg, var(--grid-line) 1px, transparent 1px);
      background-size: 46px 46px;
      mask-image: radial-gradient(circle at 50% 35%, #000, transparent 80%);
      -webkit-mask-image: radial-gradient(
        circle at 50% 35%,
        #000,
        transparent 80%
      );
    `,
    loginShell: css`
      flex: 1;
      display: grid;
      place-items: center;
      padding: 48px 24px;
      position: relative;
      z-index: 1;
    `,
    loginPanel: css`
      width: 100%;
      max-width: 420px;
      padding: 36px 32px 28px;
      background: linear-gradient(
        180deg,
        var(--bg-surface-1),
        var(--bg-surface-2)
      );
      border: 1px solid var(--border-soft);
      border-radius: 16px;
      box-shadow:
        0 24px 70px rgba(2, 6, 20, 0.55),
        inset 0 1px 0 rgba(255, 255, 255, 0.05);
      backdrop-filter: blur(14px);
    `,
    brandHeader: css`
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 14px;
      margin-bottom: 28px;
      text-align: center;
      color: var(--text-primary);
    `,
    brandSubtitle: css`
      color: var(--text-secondary);
      font-size: 12px;
      letter-spacing: 0.28em;
      text-transform: uppercase;
    `,
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
      <div className={styles.containerOverlay} />
      <div className={styles.loginShell}>
        <div className={styles.loginPanel}>
          <div className={styles.brandHeader}>
            <BrandLogo />
            <div className={styles.brandSubtitle}>Dashboard Access</div>
          </div>
          <LoginForm
            style={{ background: 'transparent', boxShadow: 'none' }}
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
