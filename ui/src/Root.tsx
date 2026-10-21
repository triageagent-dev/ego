import { ConfigProvider, theme } from 'antd'
import App from './App.tsx'

const t = {
  colorBgBase: '#1a1d24',
  colorBgContainer: '#252a36',
  colorBgElevated: '#2d3342',
  colorBorder: '#3a4052',
  colorText: '#a8b2c1',
  borderRadius: 6,
  fontSize: 14,
  fontFamily: "'Archivo', sans-serif",
  selectedBg: '#3a3358',
  selectedColor: '#b5abfc',
} as const

export function Root() {
  return (
    <ConfigProvider
      theme={{
        algorithm: theme.darkAlgorithm,
        token: {
          colorPrimary: '#b5abfc',
          colorBgBase: t.colorBgBase,
          colorBgContainer: t.colorBgContainer,
          colorBgElevated: t.colorBgElevated,
          colorBorder: t.colorBorder,
          colorText: t.colorText,
          borderRadius: t.borderRadius,
          fontSize: t.fontSize,
          fontFamily: t.fontFamily,
        },
        components: { Segmented: { itemSelectedBg: t.selectedBg, itemSelectedColor: t.selectedColor } },
      }}
    >
      <App />
    </ConfigProvider>
  )
}
