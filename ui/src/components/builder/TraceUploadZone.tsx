import React, { useCallback } from 'react';
import { css } from '@emotion/react';
import { FileJson } from 'lucide-react';
import { message } from 'antd';
import { convertTraces } from '../../api/client';
import { generateEvalSet } from '../../lib/evalset-builder';
import { useTraceContext } from '../../context/TraceContext';

export const TraceUploadZone: React.FC = () => {
  const { actions } = useTraceContext();

  const handleFileUpload = useCallback(async (file: File) => {
    try {
      const response = await convertTraces([file]);

      if (response.traces.length === 0) {
        message.error('No valid traces found in the file');
        return;
      }

      const filename = file.name.replace(/\.(jsonl?|json)$/i, '');
      const traceData = response.traces.map(t => ({ traceId: t.traceId, invocations: t.invocations }));
      const evalSet = generateEvalSet(traceData, filename);

      actions.setBuilderEvalSet(evalSet);
      message.success(`Loaded ${response.traces.length} trace(s) and generated EvalSet!`);
    } catch (error) {
      console.error('Failed to load trace:', error);
      message.error('Failed to load trace file. Please ensure it is a valid trace file.');
    }
  }, [actions]);

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    const file = e.dataTransfer.files[0];
    if (file && (file.name.endsWith('.json') || file.name.endsWith('.jsonl'))) {
      handleFileUpload(file);
    } else {
      message.error('Please upload a .json or .jsonl file');
    }
  }, [handleFileUpload]);

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
  }, []);

  const handleFileInput = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) {
      handleFileUpload(file);
    }
  }, [handleFileUpload]);

  return (
    <div css={containerStyle}>
      <div css={contentStyle}>
        <div css={headerStyle}>
          <h1>eval sets</h1>
          <p>golden references built from traces</p>
        </div>

        <div
          css={dropZoneStyle}
          onDrop={handleDrop}
          onDragOver={handleDragOver}
        >
          <input
            type="file"
            id="trace-file-input"
            accept=".json,.jsonl"
            onChange={handleFileInput}
            css={fileInputStyle}
          />
          <label htmlFor="trace-file-input" css={labelStyle}>
            <div css={iconWrapperStyle}>
              <FileJson size={22} />
            </div>
            <div>
              <h2>drop a trace file to start a new set</h2>
              <p>or click to browse · .json, .jsonl</p>
            </div>
          </label>
        </div>

      </div>
    </div>
  );
};

const containerStyle = css`
  min-height: 100%;
  background: var(--bg-primary);
  padding: 12px 18px 24px;
`;

const contentStyle = css`
  max-width: 760px;
  width: 100%;
`;

const headerStyle = css`
  display: flex;
  align-items: baseline;
  gap: 14px;
  margin-bottom: 10px;

  h1 {
    font-size: 15px;
    font-weight: 600;
    margin: 0;
    color: var(--accent-cyan);
    text-transform: lowercase;
  }

  p {
    font-size: 12px;
    color: var(--text-muted);
    margin: 0;
  }
`;

const dropZoneStyle = css`
  position: relative;
  border: 1px dashed var(--border-default);
  border-radius: var(--radius);
  padding: 16px;
  cursor: pointer;

  &:hover {
    border-color: var(--accent-primary);
    background: var(--bg-surface);
  }
`;

const fileInputStyle = css`
  display: none;
`;

const labelStyle = css`
  display: flex;
  align-items: center;
  gap: 14px;
  cursor: pointer;

  h2 {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-primary);
    margin: 0;
  }

  p {
    font-size: 12px;
    color: var(--text-muted);
    margin: 0;
  }
`;

const iconWrapperStyle = css`
  width: 44px;
  height: 44px;
  flex-shrink: 0;
  border-radius: var(--radius);
  background: var(--bg-surface);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--accent-primary);
`;


