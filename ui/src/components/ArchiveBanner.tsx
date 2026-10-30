import { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { AlertTriangle } from 'lucide-react';
import { config } from '../config';

// What /api/health says about the session archive. A failing archive means new sessions are held in memory only, so
// the person looking at the UI must be told while it is happening, not after a restart dropped them.
interface Archive {
  status: 'ok' | 'low' | 'failing';
  freePercent?: number;
  error?: string;
}

const POLL_MS = 60_000;

export function ArchiveBanner() {
  const [archive, setArchive] = useState<Archive | null>(null);

  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      try {
        const res = await fetch(config.api.endpoints.health, { cache: 'no-store' });
        if (!res.ok) return;
        const body = (await res.json()) as { data?: { archive?: Archive } };
        if (!cancelled) setArchive(body.data?.archive ?? null);
      } catch {
        // An unreachable server is the connection indicator's to say.
      }
    };
    void poll();
    const timer = setInterval(() => void poll(), POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  if (!archive || archive.status === 'ok') return null;

  const failing = archive.status === 'failing';
  return (
    <div css={bannerStyle} style={{ background: failing ? 'var(--status-failure)' : 'var(--status-warning)' }}>
      <AlertTriangle size={16} />
      <span>
        {failing
          ? `ego cannot save sessions${archive.error ? ` (${archive.error})` : ''}. New sessions are kept in memory only and are lost on a restart.`
          : `ego's session volume is running out${archive.freePercent !== undefined ? `: ${archive.freePercent}% free` : ''}.`}
      </span>
    </div>
  );
}

const bannerStyle = css`
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  z-index: 999;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 10px 16px;
  color: #fff;
  font-size: 0.8125rem;
  font-weight: 600;
`;
