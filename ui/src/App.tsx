import { TraceProvider } from './context/TraceProvider';
import { useTraceContext } from './context/TraceContext';
import { WelcomeView } from './components/welcome/WelcomeView';
import { UploadView } from './components/upload/UploadView';
import { DashboardView } from './components/dashboard/DashboardView';
import { InspectorView } from './components/inspector/InspectorView';
import { BuilderView } from './components/builder/BuilderView';
import { LiveStreamingView } from './components/streaming/LiveStreamingView';
import { AnnotationQueueView } from './components/annotation-queue/AnnotationQueueView';
import { RunsView } from './components/runs/RunsView';
import { UsageView } from './components/usage/UsageView';
import { QualityView } from './components/quality/QualityView';
import { Sidebar } from './components/sidebar/Sidebar';
import { SessionExpiredBanner } from './components/SessionExpiredBanner';

function AppContent() {
  const { state } = useTraceContext();
  const showSidebar = state.currentView !== 'welcome';

  return (
    <div style={{ display: 'flex', height: '100vh', overflow: 'hidden' }}>
      <SessionExpiredBanner />
      {showSidebar && <Sidebar />}
      <main style={{ flex: 1, overflow: 'auto', height: '100vh' }}>
        {state.currentView === 'welcome' && <WelcomeView />}
        {state.currentView === 'upload' && <UploadView />}
        {state.currentView === 'dashboard' && <DashboardView />}
        {state.currentView === 'inspector' && <InspectorView />}
        {state.currentView === 'builder' && <BuilderView />}
        {state.currentView === 'streaming' && <LiveStreamingView />}
        {state.currentView === 'annotation-queue' && <AnnotationQueueView />}
        {state.currentView === 'runs' && <RunsView />}
        {state.currentView === 'usage' && <UsageView />}
        {state.currentView === 'quality' && <QualityView />}
        {state.currentView === 'comparison' && (
          <div style={{ padding: 48, textAlign: 'center', color: 'var(--text-secondary)' }}>
            Comparison view coming soon...
          </div>
        )}
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
