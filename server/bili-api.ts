import { addWbiSign, extractWbiKey } from './wbi';

const UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)';

let wbiKeyCache: { key: string; expireAt: number } | null = null;
const WBI_KEY_TTL_MS = 11 * 3600 * 1000;

export interface DanmuHost {
  host: string;
  wss_port: number;
  ws_port: number;
  port: number;
}

export interface RoomInfo {
  roomId: number;
  buvid: string;
  token: string;
  hostList: DanmuHost[];
}

async function fetchJson(url: string, init?: RequestInit): Promise<any> {
  const res = await fetch(url, {
    headers: { 'User-Agent': UA, Referer: 'https://live.bilibili.com/' },
    ...init,
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

async function getWbiKey(): Promise<string> {
  const now = Date.now();
  if (wbiKeyCache && now < wbiKeyCache.expireAt) return wbiKeyCache.key;
  const data = await fetchJson('https://api.bilibili.com/x/web-interface/nav');
  const imgUrl = data?.data?.wbi_img?.img_url ?? '';
  const subUrl = data?.data?.wbi_img?.sub_url ?? '';
  if (!imgUrl || !subUrl) throw new Error('获取 WBI 密钥失败');
  const key = extractWbiKey(imgUrl, subUrl);
  wbiKeyCache = { key, expireAt: now + WBI_KEY_TTL_MS };
  return key;
}

async function getBuvid(): Promise<string> {
  const data = await fetchJson('https://api.bilibili.com/x/frontend/finger/spi');
  return data?.data?.b_3 ?? '';
}

export async function resolveRoomId(input: string | number): Promise<number> {
  const data = await fetchJson(
    `https://api.live.bilibili.com/room/v1/Room/get_info?room_id=${input}`,
  );
  if (data?.code !== 0) throw new Error(data?.message || '房间不存在');
  return data.data.room_id;
}

export async function getRoomInfo(input: string | number): Promise<RoomInfo> {
  const roomId = await resolveRoomId(input);
  const buvid = await getBuvid();
  const wbiKey = await getWbiKey();
  const signed = addWbiSign({ id: roomId, type: 0 }, wbiKey);
  const qs = new URLSearchParams(signed);
  const data = await fetchJson(
    `https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?${qs}`,
    { headers: { 'User-Agent': UA, Referer: 'https://live.bilibili.com/', Cookie: `buvid3=${buvid}` } },
  );
  if (data?.code !== 0) throw new Error(data?.message || '获取弹幕服务器信息失败');
  return {
    roomId,
    buvid,
    token: data.data.token,
    hostList: data.data.host_list,
  };
}
