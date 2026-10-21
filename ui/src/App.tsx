import { useEffect, useRef } from 'react';
import { TraceProvider } from './context/TraceProvider';
import { useTraceContext } from './context/TraceContext';
import { UploadView } from './components/upload/UploadView';
import { DashboardView } from './components/dashboard/DashboardView';
import { InspectorView } from './components/inspector/InspectorView';
import { BuilderView } from './components/builder/BuilderView';
import { LiveStreamingView } from './components/streaming/LiveStreamingView';
import { RunsView } from './components/runs/RunsView';
import { UsageView } from './components/usage/UsageView';
import { QualityView } from './components/quality/QualityView';
import { TabBar } from './components/shell/TabBar';
import { Sidebar } from './components/shell/Sidebar';
import { useUiMode } from './lib/mode';
import { startConnectionMonitor } from './lib/connection';
import { TABS, tabForView, viewForHash } from './components/shell/routes';
import { SessionExpiredBanner } from './components/SessionExpiredBanner';

// The current tab lives in the URL hash (#runs), so a view can be linked and
// the back button works; the keys 1-6 switch tabs and / jumps to the search box.
function useShellNavigation() {
  const { state, actions } = useTraceContext();
  const { currentView } = state;
  const { setCurrentView } = actions;
  const [mode, setMode] = useUiMode();

  // The hash is read at load and when it really changes, never when `actions` is rebuilt (it is, whenever the
  // trace files change): that re-ran this with the old hash right after a view switch and switched straight back.
  const setViewRef = useRef(setCurrentView);
  setViewRef.current = setCurrentView;
  useEffect(() => {
    const fromHash = () => {
      const view = viewForHash(location.hash);
      if (view) setViewRef.current(view);
    };
    fromHash();
    window.addEventListener('hashchange', fromHash);
    return () => window.removeEventListener('hashchange', fromHash);
  }, []);

  // Skip the first run: the hash read above has not been applied to currentView yet.
  const mounted = useRef(false);
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true;
      return;
    }
    const tab = tabForView(currentView);
    if (tab && viewForHash(location.hash) !== tab.view) {
      history.pushState(null, '', `#${tab.label}`);
    }
  }, [currentView]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const el = e.target as HTMLElement | null;
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable)) return;
      const tab = TABS.find((t) => t.key === e.key);
      if (tab) {
        setCurrentView(tab.view);
        e.preventDefault();
      } else if (e.key === 't') {
        setMode(mode === 'tui' ? 'classic' : 'tui');
        e.preventDefault();
      } else if (e.key === '/') {
        const box = document.querySelector<HTMLInputElement>('input[placeholder*="earch"]');
        if (box) {
          box.focus();
          e.preventDefault();
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [setCurrentView, mode, setMode]);
}

// How much larger the classic layout is drawn than its pixel sizes say.
const CLASSIC_ZOOM = 1.15;

function AppContent() {
  const { state } = useTraceContext();
  const [mode] = useUiMode();
  useShellNavigation();
  useEffect(() => startConnectionMonitor(), []);

  return (
    <div style={mode === 'tui'
      ? { display: 'flex', flexDirection: 'column', height: '100vh', overflow: 'hidden' }
      // The classic layout is drawn in small pixel sizes; zoom scales text, spacing and icons together, so
      // nothing ends up out of proportion. The height is divided by the zoom to still fill the window.
      : { display: 'flex', flexDirection: 'row', height: `${100 / CLASSIC_ZOOM}vh`, zoom: CLASSIC_ZOOM, overflow: 'hidden' }}>
      <SessionExpiredBanner />
      {mode === 'tui' ? <TabBar /> : <Sidebar />}
      <main style={{ flex: 1, overflow: 'auto', minHeight: 0, minWidth: 0 }}>
        {state.currentView === 'upload' && <UploadView />}
        {state.currentView === 'dashboard' && <DashboardView />}
        {state.currentView === 'inspector' && <InspectorView />}
        {state.currentView === 'builder' && <BuilderView />}
        {state.currentView === 'streaming' && <LiveStreamingView />}
        {state.currentView === 'runs' && <RunsView />}
        {state.currentView === 'usage' && <UsageView />}
        {state.currentView === 'quality' && <QualityView />}
      </main>
    </div>
  );
}

function App() {
  return (
    <TraceProvider>
      <AppContent />
    </TraceProvider>
  );
}

export default App;
