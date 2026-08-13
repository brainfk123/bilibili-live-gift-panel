export interface GiftClipExportProbe {
  codec: string;
  pixelFormat: string;
  fps: string;
  frames: number;
  duration: number;
  width: number;
  height: number;
  audioStreams: number;
  bitrate: number;
  size: number;
}

export function assertGiftClipExportProbe(kind: string, probe: GiftClipExportProbe): void;
