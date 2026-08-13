export const GIFT_CLIP_TEST_TOOL_PROVENANCE: Readonly<{
  ffmpegVersion: string;
  sourceSha256: string;
  sourceSigningFingerprint: string;
  sourceSignedTag: string;
  sourceSignedTagCommit: string;
  sourceSignedTagFingerprint: string;
  toolchainLockSha256: string;
  configureSha256: string;
}>;

export interface GiftClipTestToolPaths {
  ffmpeg: string;
  ffprobe: string;
}

export function verifyGiftClipTestTools(
  directory: string,
  options?: { runVersion?: (path: string) => string },
): GiftClipTestToolPaths;
