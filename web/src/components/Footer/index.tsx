import { GithubOutlined } from '@ant-design/icons';
import { Divider } from 'antd';
import { createStyles } from 'antd-style';
import React from 'react';

const useStyles = createStyles(({ token, css }) => ({
  footer: css`
    padding: 16px 24px;
    text-align: center;
    color: ${token.colorTextDescription};
    font-size: ${token.fontSizeSM}px;
    line-height: ${token.lineHeight};
    background: transparent;
  `,
  copyright: css`
    margin-bottom: 6px;
  `,
  link: css`
    color: ${token.colorTextDescription};
    text-decoration: none;
    transition: color ${token.motionDurationMid};

    &:hover {
      color: ${token.colorText};
    }
  `,
  meta: css`
    display: flex;
    align-items: center;
    justify-content: center;
    flex-wrap: wrap;
    gap: 6px 12px;
    font-size: ${token.fontSizeSM - 1}px;
  `,
  divider: css`
    display: inline-block;
    vertical-align: middle;
  `,
}));

const Footer: React.FC = () => {
  const { styles } = useStyles();
  const year = new Date().getFullYear();
  const repoUrl = 'https://github.com/jushjay/prism';

  return (
    <div className={styles.footer}>
      <div className={styles.copyright}>Prism &copy; {year}</div>
      <div className={styles.meta}>
        <a
          className={styles.link}
          href="https://umijs.org/"
          target="_blank"
          rel="noopener noreferrer"
        >
          Umi Max
        </a>
        <Divider type="vertical" className={styles.divider} />
        <a
          className={styles.link}
          href="https://procomponents.ant.design/"
          target="_blank"
          rel="noopener noreferrer"
        >
          Pro Components
        </a>
        <Divider type="vertical" className={styles.divider} />
        <a
          className={styles.link}
          href={repoUrl}
          target="_blank"
          rel="noopener noreferrer"
        >
          <GithubOutlined style={{ marginRight: 4 }} />
          GitHub
        </a>
      </div>
    </div>
  );
};

export default Footer;
