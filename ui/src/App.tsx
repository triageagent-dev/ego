import { useEffect, useRef } from 'react';
import { TraceProvider } from './context/TraceProvider';
import { useTraceContext } from './context/TraceContext';
import { UploadView } from './components/upload/UploadView';
import { DashboardView } from './components/dashboard/DashboardView';
import { InspectorView } from './components/inspector/InspectorView';
import { BuilderView } from './components/builder/BuilderView';
import { LiveStreamingView } from './components/streaming/LiveStreamingView';
import { AnnotationQueueView } from './components/annotation-queue/AnnotationQueueView';
import { RunsView } from './components/runs/RunsView';
import { UsageView } from './components/usage/UsageView';
import { QualityView } from './components/quality/QualityView';
import { TabBar } from './components/shell/TabBar';
import { TABS, tabForView, viewForHash } from './components/shell/routes';
import { SessionExpiredBanner } from './components/SessionExpiredBanner';

// The current tab lives in the URL hash (#runs), so a view can be linked and
// the back button works; the keys 1-6 switch tabs and / jumps to the search box.
function useShellNavigation() {
  const { state, actions } = useTraceContext();
  const { currentView } = state;
  const { setCurrentView } = actions;

  useEffect(() => {
    const fromHash = () => {
      const view = viewForHash(location.hash);
      if (view) setCurrentView(view);
    };
    fromHash();
    window.addEventListener('hashchange', fromHash);
    return () => window.removeEventListener('hashchange', fromHash);
  }, [setCurrentView]);

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
  }, [setCurrentView]);
}

function AppContent() {
  const { state } = useTraceContext();
  useShellNavigation();

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', overflow: 'hidden' }}>
      <SessionExpiredBanner />
      <TabBar />
      <main style={{ flex: 1, overflow: 'auto', minHeight: 0 }}>
        {state.currentView === 'upload' && <UploadView />}
        {state.currentView === 'dashboard' && <DashboardView />}
        {state.currentView === 'inspector' && <InspectorView />}
        {state.currentView === 'builder' && <BuilderView />}
        {state.currentView === 'streaming' && <LiveStreamingView />}
        {state.currentView === 'annotation-queue' && <AnnotationQueueView />}
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
