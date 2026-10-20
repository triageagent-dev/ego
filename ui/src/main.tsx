import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { Root } from './Root.tsx'
import { installNetworkCapture } from './lib/network-capture'
import { applyMode, storedMode } from './lib/mode'

installNetworkCapture();
// ?mode=classic (or tui) picks the style for this browser, like the switcher does.
const asked = new URLSearchParams(location.search).get('mode');
if (asked === 'classic' || asked === 'tui') applyMode(asked);
else applyMode(storedMode(), false);

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
)
