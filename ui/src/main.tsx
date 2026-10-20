import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ConfigProvider, theme } from 'antd'
import './index.css'
import App from './App.tsx'
import { installNetworkCapture } from './lib/network-capture'

installNetworkCapture();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ConfigProvider
      theme={{
        algorithm: theme.darkAlgorithm,
        token: {
          colorPrimary: '#b5abfc',
          colorBgBase: '#14161f',
          colorBgContainer: '#1b1d2b',
          colorBgElevated: '#20222f',
          colorBorder: '#3a3d55',
          colorText: '#c9ccdb',
          borderRadius: 3,
          fontSize: 13,
          fontFamily: "Menlo, 'JetBrains Mono', 'SF Mono', Consolas, monospace",
        },
        components: { Segmented: { itemSelectedBg: '#2b2547', itemSelectedColor: '#e879f9' } },
      }}
    >
      <App />
    </ConfigProvider>
  </StrictMode>,
)
