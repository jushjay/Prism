import { defineConfig } from '@umijs/max';

export default defineConfig({
  antd: {},
  access: {},
  model: {},
  initialState: {},
  request: {},
  favicons: ['/favicon.svg'],
  layout: {
    title: 'Prism',
    locale: false,
  },
  routes: [
    {
      path: '/login',
      component: './Login',
      layout: false,
    },
    {
      path: '/',
      redirect: '/overview',
    },
    {
      name: '概览',
      icon: 'DashboardOutlined',
      path: '/overview',
      component: './Overview',
    },
    {
      name: '账号池',
      icon: 'TeamOutlined',
      path: '/accounts',
      component: './Accounts',
    },
    {
      name: '用量统计',
      icon: 'LineChartOutlined',
      path: '/usage',
      component: './Usage',
    },
    {
      name: '模型管理',
      icon: 'PartitionOutlined',
      path: '/models',
      component: './Models',
    },
    {
      name: 'IP 防火墙',
      icon: 'SafetyCertificateOutlined',
      path: '/security',
      component: './Security',
    },
    {
      name: '运行参数',
      icon: 'SettingOutlined',
      path: '/settings',
      component: './Settings',
    },
    {
      name: 'API 示例',
      icon: 'CodeOutlined',
      path: '/examples',
      component: './Examples',
    },
  ],
  proxy: {
    '/health': {
      target: 'http://localhost:8080',
      changeOrigin: true,
    },
    '/auth': {
      target: 'http://localhost:8080',
      changeOrigin: true,
    },
    '/admin': {
      target: 'http://localhost:8080',
      changeOrigin: true,
    },
    '/v1': {
      target: 'http://localhost:8080',
      changeOrigin: true,
    },
  },
  npmClient: 'pnpm',
  utoopack: {},
});
