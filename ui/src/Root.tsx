import { ConfigProvider, theme } from 'antd'
import App from './App.tsx'
import { useUiMode } from './lib/mode'

const MONO = "Menlo, 'JetBrains Mono', 'SF Mono', Consolas, monospace";

const THEMES = {
  tui: {
    colorBgBase: '#14161f',
    colorBgContainer: '#1b1d2b',
    colorBgElevated: '#20222f',
    colorBorder: '#3a3d55',
    colorText: '#c9ccdb',
    borderRadius: 3,
    fontSize: 13,
    fontFamily: MONO,
    selectedBg: '#2b2547',
    selectedColor: '#e879f9',
  },
  classic: {
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
  },
} as const

export function Root() {
  const [mode] = useUiMode()
  const t = THEMES[mode]
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
