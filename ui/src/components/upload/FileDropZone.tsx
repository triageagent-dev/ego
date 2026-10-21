import React from 'react';
import { Upload } from 'antd';
import type { UploadProps } from 'antd';
import { FileJson } from 'lucide-react';
import { css } from '@emotion/react';

const { Dragger } = Upload;

interface FileDropZoneProps {
  title: string;
  description: string;
  multiple?: boolean;
  onChange: (files: File[]) => void | Promise<void>;
  accept?: string;
}

// One slim row: the icon, the title and the hint side by side.
const dropZoneStyle = css`
  .ant-upload.ant-upload-drag {
    border: 1px dashed var(--border-default);
    border-radius: var(--radius);
    transition: all 0.3s ease;

    &:hover {
      border-color: var(--accent-primary);
      background-color: var(--bg-elevated);
    }
  }

  .ant-upload.ant-upload-drag .ant-upload-btn {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    gap: 2px 10px;
    padding: 6px 12px !important;
  }

  .ant-upload-drag-icon {
    display: inline-flex;
    margin: 0 !important;
    color: var(--accent-primary);
  }

  .ant-upload-text {
    color: var(--text-primary) !important;
    font-size: 12.5px !important;
    font-weight: 500;
    margin: 0 !important;
  }

  .ant-upload-hint {
    color: var(--text-secondary) !important;
    font-size: 11.5px !important;
    margin: 0 !important;
  }
`;

export const FileDropZone: React.FC<FileDropZoneProps> = ({
  title,
  description,
  multiple = false,
  onChange,
  accept = '.json',
}) => {
  const uploadProps: UploadProps = {
    name: 'file',
    multiple,
    accept,
    beforeUpload: (file, fileList) => {
      // Handle file selection
      if (multiple) {
        onChange(fileList);
      } else {
        onChange([file]);
      }
      // Prevent auto upload
      return false;
    },
    onRemove: () => {
      onChange([]);
    },
    showUploadList: false,
  };

  return (
    <div css={dropZoneStyle}>
      <Dragger {...uploadProps}>
        <p className="ant-upload-drag-icon">
          <FileJson size={15} />
        </p>
        <p className="ant-upload-text">{title}</p>
        <p className="ant-upload-hint">{description}</p>
      </Dragger>
    </div>
  );
};
