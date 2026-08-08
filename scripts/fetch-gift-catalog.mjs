import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const API_URL = 'https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig?platform=pc&room_id=1&area_id=1&biz_code=live';
const OUT = fileURLToPath(new URL('../src/data/gift-catalog.json', import.meta.url));

async function main() {
  let list = [];
  try {
    const res = await fetch(API_URL, {
      headers: { 'User-Agent': 'Mozilla/5.0', Referer: 'https://live.bilibili.com/' },
    });
    const json = await res.json();
    list = (json?.data?.list ?? []).map((g) => ({
      id: g.id,
      name: g.name,
      price: g.price,
      coinType: g.coin_type === 'gold' ? 'gold' : 'silver',
      imgBasic: g.img_basic ?? '',
      ...((g.gift_type === 6 || g.gift_attrs?.includes?.(6)) ? { requiresLogin: true } : {}),
    }));
  } catch (err) {
    console.error('fetch gift catalog failed:', err.message);
    process.exitCode = 1;
  }
  mkdirSync(dirname(OUT), { recursive: true });
  writeFileSync(OUT, JSON.stringify(list, null, 2), 'utf-8');
  console.log(`gift catalog: ${list.length} gifts -> ${OUT}`);
}

main();
