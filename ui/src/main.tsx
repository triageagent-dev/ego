import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { installConsoleCapture } from './lib/console-capture'
import { installNetworkCapture } from './lib/network-capture'

installConsoleCapture();
installNetworkCapture();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
