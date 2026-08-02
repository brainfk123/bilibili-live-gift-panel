export interface GiftEvent {
  giftId: number;
  giftName: string;
  num: number;
  price: number;
  coinType: 'gold' | 'silver';
  totalCoin: number;
  uname: string;
  avatar?: string;
  uid: number;
  timestamp: number;
  imgBasic: string;
  rnd: string;
}

export interface ScEvent {
  id: number;
  price: number;
  message: string;
  uname: string;
  uid: number;
  giftId: number;
  giftName: string;
}

export function parseGift(data: any): GiftEvent {
  return {
    giftId: data.giftId ?? 0,
    giftName: data.giftName ?? '',
    num: data.num ?? 1,
    price: data.price ?? 0,
    coinType: data.coin_type === 'gold' ? 'gold' : 'silver',
    totalCoin: data.total_coin ?? 0,
    uname: data.uname ?? '',
    avatar: data.face ?? '',
    uid: data.uid ?? 0,
    timestamp: data.timestamp ?? Math.floor(Date.now() / 1000),
    imgBasic: data.gift_info?.img_basic ?? '',
    rnd: String(data.rnd ?? `${data.timestamp ?? ''}-${data.uid ?? ''}-${data.giftId ?? ''}`),
  };
}

export function parseSc(data: any): ScEvent {
  return {
    id: data.id ?? 0,
    price: data.price ?? 0,
    message: data.message ?? '',
    uname: data.user_info?.uname ?? '',
    uid: data.uid ?? 0,
    giftId: data.gift?.gift_id ?? 0,
    giftName: data.gift?.gift_name ?? '醒目留言',
  };
}
