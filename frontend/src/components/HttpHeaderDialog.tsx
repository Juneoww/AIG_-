import React, { useEffect, useState } from 'react';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2, X } from 'lucide-react';
import { Button } from './ui/button';
import { Input } from './ui/input';
import { FIXED_AI_PORTS, FIXED_AI_PORT_COUNT, type PortMode } from '../types/scanConfig';

interface HttpHeader {
  key: string;
  value: string;
}

interface HttpHeaderDialogProps {
  open: boolean;
  headers: HttpHeader[];
  portMode: PortMode;
  showPortMode: boolean;
  onConfirm: (headers: HttpHeader[], portMode: PortMode) => void;
  onCancel: () => void;
}

const HttpHeaderDialog: React.FC<HttpHeaderDialogProps> = ({
  open,
  headers,
  portMode,
  showPortMode,
  onConfirm,
  onCancel,
}) => {
  const { t } = useTranslation();
  const [localHeaders, setLocalHeaders] = useState<HttpHeader[]>([]);
  const [localPortMode, setLocalPortMode] = useState<PortMode>(portMode);

  useEffect(() => {
    if (open) {
      setLocalHeaders(headers.map(header => ({ ...header })));
      setLocalPortMode(portMode);
    }
  }, [open, headers, portMode]);

  const addHeader = () => {
    setLocalHeaders(current => [...current, { key: '', value: '' }]);
  };

  const removeHeader = (index: number) => {
    setLocalHeaders(current => current.filter((_, headerIndex) => headerIndex !== index));
  };

  const updateHeader = (index: number, field: keyof HttpHeader, value: string) => {
    setLocalHeaders(current => current.map((header, headerIndex) =>
      headerIndex === index ? { ...header, [field]: value } : header
    ));
  };

  const handleConfirm = () => {
    const validHeaders = localHeaders.filter(header => header.key.trim());
    onConfirm(validHeaders, localPortMode);
  };

  const fixedPorts = FIXED_AI_PORTS
    .split(',')
    .map(port => port.replace('-', '–'))
    .join(t('httpHeaderDialog.portSeparator'));

  return (
    <DialogPrimitive.Root open={open} onOpenChange={isOpen => !isOpen && onCancel()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/50" />
        <DialogPrimitive.Content className="fixed left-1/2 top-1/2 z-50 max-h-[calc(100vh-2rem)] w-[calc(100vw-2rem)] max-w-[560px] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border border-gray-200 bg-white p-5 shadow-xl sm:p-6">
          <div className="mb-5 flex items-center justify-between gap-4">
            <DialogPrimitive.Title className="text-lg font-semibold text-gray-900">
              {showPortMode ? t('httpHeaderDialog.scanTitle') : t('httpHeaderDialog.title')}
            </DialogPrimitive.Title>
            <DialogPrimitive.Close asChild>
              <Button type="button" size="sm" variant="ghost" className="h-8 w-8 p-1" aria-label={t('httpHeaderDialog.close')}>
                <X className="h-4 w-4" />
              </Button>
            </DialogPrimitive.Close>
          </div>
          <DialogPrimitive.Description className="sr-only">
            {showPortMode ? t('httpHeaderDialog.portModeScope') : t('httpHeaderDialog.headersTitle')}
          </DialogPrimitive.Description>

          {showPortMode && (
            <fieldset className="mb-5">
              <legend className="text-sm font-semibold text-gray-900">{t('httpHeaderDialog.portModeTitle')}</legend>
              <p className="mb-3 mt-1 text-xs text-gray-500">{t('httpHeaderDialog.portModeScope')}</p>
              <div className="overflow-hidden rounded-lg border border-gray-200">
                <label className={`flex cursor-pointer items-start gap-3 border-b border-gray-200 px-4 py-3 transition-colors ${localPortMode === 'full_tcp' ? 'bg-blue-50' : 'hover:bg-gray-50'}`}>
                  <input
                    type="radio"
                    name="scan-port-mode"
                    value="full_tcp"
                    checked={localPortMode === 'full_tcp'}
                    onChange={() => setLocalPortMode('full_tcp')}
                    className="mt-0.5 h-4 w-4 shrink-0 accent-blue-600"
                  />
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold text-gray-900">
                      {t('httpHeaderDialog.fullTcp')}
                      <span className="ml-2 text-xs font-normal text-gray-500">{t('httpHeaderDialog.defaultMode')}</span>
                    </span>
                    <span className="mt-1 block text-xs text-gray-600">{t('httpHeaderDialog.fullTcpDetail')}</span>
                  </span>
                </label>
                <label className={`flex cursor-pointer items-start gap-3 px-4 py-3 transition-colors ${localPortMode === 'fixed_ai' ? 'bg-blue-50' : 'hover:bg-gray-50'}`}>
                  <input
                    type="radio"
                    name="scan-port-mode"
                    value="fixed_ai"
                    checked={localPortMode === 'fixed_ai'}
                    onChange={() => setLocalPortMode('fixed_ai')}
                    className="mt-0.5 h-4 w-4 shrink-0 accent-blue-600"
                  />
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold text-gray-900">
                      {t('httpHeaderDialog.fixedAi')}
                      <span className="ml-2 text-xs font-normal text-gray-500">{t('httpHeaderDialog.fixedAiCount', { portCount: FIXED_AI_PORT_COUNT })}</span>
                    </span>
                    <span className="mt-1 block break-words text-xs font-medium leading-relaxed text-blue-800">{fixedPorts}</span>
                  </span>
                </label>
              </div>
            </fieldset>
          )}

          <section aria-label={t('httpHeaderDialog.headersTitle')}>
            {showPortMode && <h4 className="mb-3 text-sm font-semibold text-gray-900">{t('httpHeaderDialog.headersTitle')}</h4>}
            <div className="mb-4 space-y-3">
              {localHeaders.map((header, index) => (
                <div key={index} className="flex flex-col gap-2 sm:flex-row sm:items-center">
                  <Input
                    aria-label={t('httpHeaderDialog.headerName')}
                    placeholder={t('httpHeaderDialog.headerName')}
                    value={header.key}
                    onChange={event => updateHeader(index, 'key', event.target.value)}
                    className="sm:flex-1"
                  />
                  <Input
                    aria-label={t('httpHeaderDialog.headerValue')}
                    placeholder={t('httpHeaderDialog.headerValue')}
                    value={header.value}
                    onChange={event => updateHeader(index, 'value', event.target.value)}
                    className="sm:flex-1"
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => removeHeader(index)}
                    className="h-8 w-8 self-end p-1 text-red-500 hover:text-red-700 sm:self-auto"
                    aria-label={`${t('httpHeaderDialog.removeHeader')} ${index + 1}`}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
            <Button type="button" onClick={addHeader} variant="outline" className="mb-5 w-full">
              <Plus className="mr-2 h-4 w-4" />
              {t('httpHeaderDialog.addHeader')}
            </Button>
          </section>

          <div className="flex flex-col-reverse gap-2 sm:flex-row">
            <DialogPrimitive.Close asChild>
              <Button type="button" variant="outline" className="flex-1">{t('common.cancel')}</Button>
            </DialogPrimitive.Close>
            <Button type="button" onClick={handleConfirm} className="flex-1 bg-blue-600 hover:bg-blue-700">
              {t('httpHeaderDialog.save')}
            </Button>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
};

export default HttpHeaderDialog;
