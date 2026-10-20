import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
// EGO_DEV_BACKEND points the dev server's proxy at a running ego (default :8001); run it with
// VITE_API_BASE_URL= and VITE_WS_URL=ws://localhost:5173 to go through the proxy instead of CORS.
const backend = process.env.EGO_DEV_BACKEND ?? 'http://localhost:8001'

export default defineConfig({
  server: {
    proxy: {
      '/api': backend,
      '/auth': backend,
      '/stream': backend,
      '/ws': { target: backend, ws: true },
    },
  },
  plugins: [
    react({
      jsxImportSource: '@emotion/react',
      babel: {
        plugins: ['@emotion/babel-plugin'],
      },
    }),
  ],
})
